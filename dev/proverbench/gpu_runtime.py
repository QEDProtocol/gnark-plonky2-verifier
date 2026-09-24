"""Read-only GPU inventory and explicit, single-GPU sandbox configuration."""
import csv
import argparse
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import time

import lab


def query(kind, fields, uuid=None):
    cmd = ['nvidia-smi', '--query-' + kind + '=' + ','.join(fields), '--format=csv,noheader,nounits']
    if uuid:
        cmd += ['--id=' + uuid]
    raw = subprocess.check_output(cmd, text=True, stderr=subprocess.DEVNULL, timeout=5)
    result = []
    for row in csv.reader(raw.splitlines()):
        if len(row) != len(fields):
            raise ValueError('unexpected nvidia-smi schema')
        result.append(dict(zip(fields, (x.strip() for x in row))))
    return result


def select_gpu(uuid, architecture):
    if not re.fullmatch(r'GPU-[0-9a-fA-F-]{36}', uuid):
        raise ValueError('supply a full --gpu-uuid, not a mutable device index')
    fields = ['uuid', 'name', 'driver_version', 'compute_cap', 'memory.total', 'memory.used', 'utilization.gpu']
    cards = query('gpu', fields, uuid)
    if len(cards) != 1 or cards[0]['uuid'] != uuid:
        raise ValueError('GPU UUID selection mismatch')
    card = cards[0]
    if card['compute_cap'].replace('.', '') != architecture:
        raise ValueError('GPU architecture does not match compiled CUDA kernels')
    minor = None
    for path in Path('/proc/driver/nvidia/gpus').glob('*/information'):
        info = dict((k.strip(), v.strip()) for line in path.read_text().splitlines()
                    for k, sep, v in [line.partition(':')] if sep)
        if info.get('GPU UUID') == uuid:
            minor = int(info['Device Minor'])
    if minor is None or minor < 0:
        raise ValueError('cannot map GPU UUID to NVIDIA device node')
    nodes = [f'/dev/nvidia{minor}', '/dev/nvidiactl', '/dev/nvidia-uvm']
    for node in nodes:
        if not stat.S_ISCHR(Path(node).stat().st_mode):
            raise ValueError('required NVIDIA character device missing')
    card['device_nodes'] = nodes
    card['cuda_local_ordinal'] = 0
    return card


def validate_libraries(build):
    if not build.get('cuda_backend_built'):
        raise ValueError('CUDA kernels missing; build with gpu_build.py build --variant icicle-msm --cuda')
    libdir = Path(build['library_dir']).resolve()
    if not libdir.is_relative_to(lab.ROOT / 'lab-deps'):
        raise ValueError('CUDA libraries escaped experiment directory')
    actual = {p.relative_to(libdir).as_posix(): lab.digest(p) for p in sorted(libdir.rglob('*.so'))}
    if actual != build['dispatch_library_sha256']:
        raise ValueError('CUDA library set changed; rebuild before running')
    return libdir


def sandbox_args(build, card):
    libdir = validate_libraries(build)
    # Curve backend depends on a field backend in a nested directory. Do not
    # rely on recursive dlopen traversal order to make that dependency visible.
    library_dirs = sorted({str((libdir / name).parent) for name in build['dispatch_library_sha256']})
    library_path = os.pathsep.join([str(libdir), *library_dirs, build['cuda']['toolkit_libdir']])
    flags = []
    for node in card['device_nodes']:
        flags += ['--dev-bind', node, node]
    flags += ['--ro-bind', str(libdir), str(libdir),
              '--setenv', 'CUDA_VISIBLE_DEVICES', card['uuid'],
              '--setenv', 'CUDA_DEVICE_ORDER', 'PCI_BUS_ID',
              '--setenv', 'CUDA_CACHE_DISABLE', '1',
              '--setenv', 'LD_LIBRARY_PATH', library_path]
    return flags


def observe(uuid, pid, stop, output, period):
    """Device-wide occupancy and matching process allocation, never GPU memory contents."""
    with output.open('x') as f:
        while not stop.is_set():
            row = {'monotonic_s': time.monotonic(), 'uuid': uuid, 'pid': pid}
            try:
                fields = ['memory.used', 'memory.total', 'utilization.gpu', 'temperature.gpu', 'power.draw']
                rows = query('gpu', fields, uuid)
                if len(rows) != 1:
                    raise ValueError('GPU disappeared')
                row['device'] = rows[0]
                # This is a different scope from device-wide used memory.
                apps = query('compute-apps', ['pid', 'gpu_uuid', 'used_memory'])
                row['process_used_mib'] = [a['used_memory'] for a in apps
                                           if a['gpu_uuid'] == uuid and a['pid'] == str(pid)]
                row['other_compute_processes'] = sum(a['gpu_uuid'] == uuid and a['pid'] != str(pid) for a in apps)
            except (OSError, ValueError, subprocess.SubprocessError):
                row['observation_unavailable'] = True
            f.write(json.dumps(row) + '\n')
            f.flush()
            stop.wait(period)


def summarize(path):
    rows = [json.loads(line) for line in path.read_text().splitlines()]
    def numbers(values):
        result = []
        for value in values:
            try:
                result.append(float(value))
            except (TypeError, ValueError):
                pass
        return result
    device = numbers(r.get('device', {}).get('memory.used') for r in rows)
    process = numbers(v for r in rows for v in r.get('process_used_mib', []))
    return {'samples': len(rows), 'unavailable_samples': sum(bool(r.get('observation_unavailable')) for r in rows),
            'sampled_device_peak_used_mib': max(device) if device else None,
            'sampled_process_peak_used_mib': max(process) if process else None,
            'other_compute_processes_observed': any(r.get('other_compute_processes', 0) > 0 for r in rows),
            'notes': ['Sampled peaks may miss short allocation peaks.',
                      'Device memory includes other processes; process memory is NVML-reported allocation.',
                      'Sampling starts after backend initialization and includes parameter loading/warm-up.']}


def check(args):
    """Explicit synthetic GPU correctness workload; no archived inputs loaded."""
    from gpu_build import verify_dependencies
    root = lab.ROOT / 'lab-bin/icicle-cuda'
    build = json.loads((root / 'build.json').read_text())
    if verify_dependencies(True) != build['dependency_sha256'] or lab.source_digest() != build['source_sha256']:
        raise ValueError('source/dependencies changed; rebuild before gpu-check')
    binary = root / 'proverbench.test'
    if lab.digest(binary) != build.get('test_binary_sha256'):
        raise ValueError('compile tests first or test binary changed')
    card = select_gpu(args.gpu_uuid, build['cuda']['architecture'])
    if not args.name or Path(args.name).name != args.name or args.name in ('.', '..'):
        raise ValueError('name must be a single directory component')
    out = lab.ROOT / 'lab-private' / args.name
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    lab.write_json(out / 'check.json', {'build': build, 'gpu': card, 'synthetic_only': True,
                                      'pipeline': args.pipeline, 'chunk_timing': args.chunk_timing,
                                      'ntt': args.ntt, 'h': args.h, 'h_large': args.h_large,
                                      'domain_memory': args.domain_memory})
    test_filter = '^(TestICICLE|TestMSMHook)'
    if args.pipeline:
        test_filter = '^TestICICLEMSMPipeline$'
    elif args.chunk_timing:
        test_filter = '^TestICICLEMSMChunkTiming$'
    elif args.domain_memory:
        test_filter = '^TestICICLENTTDomainMemory$'
    elif args.h or args.h_large:
        test_filter = '^TestICICLEComputeH$'
    elif args.ntt:
        test_filter = '^TestICICLENTTVectors$'
    cmd = ['bwrap', '--die-with-parent', '--new-session', '--unshare-net',
           '--ro-bind', '/', '/', '--bind', str(out), str(out), '--tmpfs', '/tmp',
           '--proc', '/proc', '--dev', '/dev', '--clearenv',
           '--setenv', 'HOME', '/tmp', '--setenv', 'GOMAXPROCS', '8' if args.h_large else '2',
           *sandbox_args(build, card), '--setenv', 'PROVERBENCH_GPU_TEST', '1',
           '--setenv', 'PROVERBENCH_BACKEND_DIR', str(Path(build['library_dir']) / 'backend'),
           '--setenv', 'PROVERBENCH_PIPELINE', '1' if args.pipeline else '0',
           '--setenv', 'PROVERBENCH_CHUNK_TIMING', '1' if args.chunk_timing else '0',
           '--setenv', 'PROVERBENCH_H_TEST', 'large' if args.h_large else ('small' if args.h else ''),
           '--setenv', 'PROVERBENCH_DOMAIN_TEST', '1' if args.domain_memory else '0',
           '--setenv', 'PROVERBENCH_NTT_TEST', '1' if args.ntt else '0',
           '--', str(binary), '-test.v', '-test.timeout=10m',
           '-test.run=' + test_filter]
    # Only synthetic vectors are used here; no real proof/witness can be logged.
    with (out / 'check.log').open('x') as log:
        result = subprocess.run(cmd, stdout=log, stderr=subprocess.STDOUT)
    lab.write_json(out / 'exit.json', {'exit_code': result.returncode})
    print(json.dumps({'check': str(out), 'passed': result.returncode == 0}))
    return result.returncode


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description='Explicit single-GPU synthetic correctness check')
    parser.add_argument('--gpu-uuid', required=True)
    parser.add_argument('--name', required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--pipeline', action='store_true')
    mode.add_argument('--chunk-timing', action='store_true')
    mode.add_argument('--h', action='store_true')
    mode.add_argument('--domain-memory', action='store_true')
    mode.add_argument('--h-large', action='store_true')
    mode.add_argument('--ntt', action='store_true')
    raise SystemExit(check(parser.parse_args()))
