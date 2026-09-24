#!/usr/bin/env python3
"""Explicit build/run/report commands. No proof workload runs on import/build."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import statistics
import subprocess
import sys
import threading
import time

ROOT = Path(__file__).resolve().parents[2]
WORKSPACE = ROOT.parent
DEFAULT_GO = WORKSPACE / 'tmp/prove-proxy-replay-20260921/toolchain1223/go/bin/go'
DEFAULT_DATA = WORKSPACE / 'tmp/prove-proxy-replay-20260921/private/artifacts'
GROUPS = {'bridge': '', 'deposit': 'deposit_append', 'withdrawal': 'withdrawal_claim'}
KEYS = ('circuit_groth16.bin', 'pk_groth16.bin', 'vk_groth16.bin')
INPUTS = ('common_circuit_data.json', 'proof_with_public_inputs.json', 'verifier_only_circuit_data.json')


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(4 * 1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def write_json(path, value):
    with path.open('x', encoding='utf8') as f:
        json.dump(value, f, indent=2, ensure_ascii=False)
        f.write('\n')


def source_digest():
    # Include tracked files and new lab code, exclude ignored private outputs.
    names = subprocess.check_output(['git', 'ls-files', '-co', '--exclude-standard', '-z'], cwd=ROOT).split(b'\0')
    h = hashlib.sha256()
    for name in sorted(set(names) - {b''}):
        p = ROOT / os.fsdecode(name)
        if p.is_file():
            h.update(name + b'\0' + digest(p).encode() + b'\n')
    return h.hexdigest()


def build(args):
    go = Path(args.go).resolve()
    version = subprocess.check_output([str(go), 'version'], text=True).strip()
    if ' go1.22.3 ' not in version:
        raise ValueError('CPU baseline requires Go 1.22.3; use a separate build recipe for another toolchain')
    out = ROOT / 'lab-bin'
    out.mkdir(exist_ok=True)
    cache = ROOT / 'lab-cache'
    cache.mkdir(exist_ok=True)
    env = {k: os.environ[k] for k in ('HOME', 'PATH', 'TMPDIR') if k in os.environ}
    env.update(GOTOOLCHAIN='local', GOCACHE=str(cache), GOPROXY='off', GOSUMDB='off',
               GOMODCACHE=str(Path(args.modcache).resolve()), GOMAXPROCS='1', CGO_ENABLED='0', GOENV='off')
    cmd = [str(go), 'build', '-p', '1', '-mod=readonly', '-trimpath', '-tags=proverbench',
           '-o', str(out / 'proverbench'), './cmd/proverbench']
    subprocess.run(['nice', '-n', '19', *cmd], cwd=ROOT, env=env, check=True)
    if args.compile_tests:
        subprocess.run(['nice', '-n', '19', str(go), 'test', '-c', '-p', '1', '-mod=readonly',
                        '-tags=proverbench', '-o', str(out / 'proverbench.test'), './cmd/proverbench'],
                       cwd=ROOT, env=env, check=True)
    manifest = {'base_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                'source_sha256': source_digest(), 'binary_sha256': digest(out / 'proverbench'),
                'go_version': version, 'go_binary_sha256': digest(go), 'build_command': cmd,
                'backend': 'cpu', 'production_binary': False, 'cgo_enabled': False}
    # Build output may be replaced explicitly; run artifacts never are.
    (out / 'build.json').write_text(json.dumps(manifest, indent=2) + '\n')
    print(json.dumps({'built': str(out / 'proverbench'), 'go_version': version, 'workload_executed': False}))


def select_samples(manifest, scenario, sample_id):
    if scenario not in ('all', *GROUPS):
        raise ValueError('invalid scenario')
    seen, samples = set(), []
    for s in manifest['ffi_samples']:
        name = s['id']
        if not name or name in ('.', '..') or Path(name).name != name or '\\' in name or '\0' in name:
            raise ValueError('invalid sample ID')
        if name in seen or s['scenario'] not in GROUPS:
            raise ValueError('invalid fixture manifest')
        seen.add(name)
        if scenario in ('all', s['scenario']) and (not sample_id or sample_id == name):
            samples.append({'id': name, 'scenario': s['scenario']})
    if not samples:
        raise ValueError('no matching samples')
    return samples


def check_data(root, samples, manifest):
    """Hash only the selected fixture inputs/parameters, never output contents."""
    root = root.resolve()
    expected = {x['name']: x for x in manifest['files']}
    names = set()
    for s in samples:
        names.update(f'ffi/{s["id"]}/{name}' for name in INPUTS)
        names.update((Path('keystore') / GROUPS[s['scenario']] / name).as_posix() for name in KEYS)
    checked = {}
    for name in sorted(names):
        p = (root / name).resolve()
        if not p.is_relative_to(root) or name not in expected:
            raise ValueError('fixture escaped root or lacks recorded digest')
        sha = digest(p)
        if p.stat().st_size != expected[name]['size'] or sha != expected[name]['sha256']:
            raise ValueError(f'fixture integrity mismatch: {name}')
        checked[name] = sha
    return checked


def observe(pid, stop, output, period):
    """External samples from /proc; no debugger, heap reads or forced GC."""
    ticks = os.sysconf('SC_CLK_TCK')
    with output.open('x') as f:
        while not stop.is_set():
            try:
                base = Path('/proc') / str(pid)
                row = {'monotonic_s': time.monotonic(), 'pid': pid}
                stat = (base / 'stat').read_text().rsplit(')', 1)[1].split()
                row.update(user_cpu_s=int(stat[11]) / ticks, system_cpu_s=int(stat[12]) / ticks)
                # Host counters let analysis distinguish prover work from other
                # CPU workloads without reading any process arguments/payloads.
                try:
                    host = [int(x) for x in Path('/proc/stat').read_text().splitlines()[0].split()[1:9]]
                    row['host_cpu_total_s'] = sum(host) / ticks
                    row['host_cpu_busy_s'] = (sum(host) - host[3] - host[4]) / ticks
                    row['host_loadavg_1m'] = float(Path('/proc/loadavg').read_text().split()[0])
                except (OSError, ValueError, IndexError):
                    row['host_cpu_unavailable'] = True
                for file, allowed in [('status', {'VmRSS': 'rss_bytes', 'VmHWM': 'rss_hwm_bytes', 'VmSwap': 'swap_bytes', 'Threads': 'threads'}),
                                      ('smaps_rollup', {'Pss': 'pss_bytes', 'Anonymous': 'anonymous_bytes', 'Private_Dirty': 'private_dirty_bytes'})]:
                    try:
                        for line in (base / file).read_text().splitlines():
                            key, _, value = line.partition(':')
                            if key in allowed:
                                row[allowed[key]] = int(value.split()[0]) * (1 if key == 'Threads' else 1024)
                    except (OSError, ValueError):
                        row[file + '_unavailable'] = True
                f.write(json.dumps(row) + '\n'); f.flush()
            except (OSError, ValueError, IndexError):
                break
            stop.wait(period)


def percentile(values, quantile):
    return sorted(values)[max(0, math.ceil(len(values) * quantile) - 1)]


def summarize(events, metadata, exit_code):
    measured = [e for e in events if e['event'] == 'proof' and not e['context']['warmup'] and e.get('verified') is True]
    expected = metadata['config']['cycles'] * len(metadata['samples'])
    warm = [e for e in events if e['event'] == 'proof' and e['context']['warmup'] and e.get('verified') is True]
    errors = [e.get('code') for e in events if e['event'] == 'error']
    complete = (exit_code == 0 and not errors and len(measured) == expected
                and len(warm) == metadata['config']['warmup'] * len(metadata['samples'])
                and any(e['event'] == 'finished' for e in events))
    successful = {e['context']['sequence'] for e in measured}
    by_stage = {}
    for e in events:
        c = e.get('context', {})
        if c.get('sequence') not in successful or c.get('warmup', True):
            continue
        if e['event'] not in ('proof', 'stage', 'internal_stage') or e.get('ok') is False:
            continue
        if 'duration_ms' not in e:
            continue
        key = c['scenario'] + '/' + ('total' if e['event'] == 'proof' else e['stage'])
        by_stage.setdefault(key, []).append(e['duration_ms'])
    stats = {k: {'n': len(v), 'median_ms': statistics.median(v), 'p95_ms': percentile(v, .95),
                 'min_ms': min(v), 'max_ms': max(v)} for k, v in sorted(by_stage.items())}
    return {'complete': complete, 'exit_code': exit_code, 'expected_measured': expected,
            'verified_measured': len(measured), 'verified_warmup': len(warm), 'errors': errors,
            'stages': stats,
            'peak_observed_rss_bytes': max((e.get('rss_bytes', 0) for e in events), default=0),
            'rss_hwm_bytes': max((e.get('rss_hwm_bytes', 0) for e in events), default=0),
            'notes': ['Warm-up excluded from timings; loading excluded from total.',
                      'solver/prover_compute are nested in groth16_prove; compute_h is nested in prover_compute; do not sum them twice.',
                      'Total includes benchmark event output; not full RPC/FFI latency.',
                      'RSS/HWM include parameter loading and warm-up; small-sample P95 is descriptive only.']}


def run(args):
    if args.cycles < 1 or args.warmup < 0 or args.interval < 0 or args.sample_interval < 0 or args.gomaxprocs < 1:
        raise ValueError('invalid run configuration')
    gpu = args.backend == 'icicle-msm'
    if args.msm_internal_chunks not in (1,2,4,8):
        raise ValueError('invalid internal chunks')
    if args.cpu_control and args.backend != 'cpu-msm':
        raise ValueError('--cpu-control requires --backend cpu-msm')
    if not 1 <= args.msm_chunk_size <= 1 << 20 or args.gpu_sample_interval < 0:
        raise ValueError('invalid GPU configuration')
    if not gpu and args.gpu_uuid:
        raise ValueError('--gpu-uuid requires icicle-msm backend')
    build_dir = ROOT / 'lab-bin'
    if args.backend != 'cpu':
        build_dir /= 'icicle-cuda' if gpu or args.cpu_control else 'cpu-msm'
    binary = build_dir / 'proverbench'
    build_info = json.loads((build_dir / 'build.json').read_text())
    if build_info['backend'] != ('icicle-msm' if args.cpu_control else args.backend):
        raise ValueError('requested backend does not match build')
    if args.backend != 'cpu':
        from gpu_build import verify_dependencies
        if verify_dependencies(gpu or args.cpu_control) != build_info['dependency_sha256']:
            raise ValueError('dependency changed; rebuild before running')
    if digest(binary) != build_info['binary_sha256'] or source_digest() != build_info['source_sha256']:
        raise ValueError('source or binary changed; rebuild before running')
    card, gpu_flags = None, []
    if args.cpu_control:
        import gpu_runtime
        gpu_runtime.validate_libraries(build_info)
    if gpu:
        import gpu_runtime
        gpu_runtime.validate_libraries(build_info)
        card = gpu_runtime.select_gpu(args.gpu_uuid, build_info['cuda']['architecture'])
        gpu_flags = gpu_runtime.sandbox_args(build_info, card)
    root = Path(args.artifacts).resolve()
    manifest = json.loads((root / 'manifest.json').read_text())
    samples = select_samples(manifest, args.scenario, args.sample)
    hashes = check_data(root, samples, manifest)
    if not shutil.which('bwrap'):
        raise ValueError('bubblewrap required for read-only/network isolation')
    out = ROOT / 'lab-private' / args.name
    if not args.name or Path(args.name).name != args.name or args.name in ('.', '..'):
        raise ValueError('name must be a single directory component')
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    # Preserve the exact executable for later profiling and reproducibility.
    shutil.copy2(binary, out / 'proverbench')
    config = {k: getattr(args, k) for k in ('backend', 'scenario', 'sample', 'cycles', 'warmup', 'interval',
              'sample_interval', 'gomaxprocs', 'gogc', 'gomemlimit', 'gc_between', 'profile',
              'gpu_uuid', 'msm_chunk_size', 'msm_internal_chunks', 'gpu_sample_interval', 'cpu_control')}
    info = {'config': config, 'samples': samples, 'input_sha256': hashes, 'build': build_info,
            'host': os.uname().nodename, 'kernel': os.uname().release,
            'cpu_model': next((x.split(':', 1)[1].strip() for x in Path('/proc/cpuinfo').read_text().splitlines() if x.startswith('model name')), 'unknown'),
            'affinity': sorted(os.sched_getaffinity(0)), 'started_at_unix': time.time(), 'gpu': card}
    write_json(out / 'run.json', info)
    cmd = ['bwrap', '--die-with-parent', '--new-session', '--unshare-net', '--ro-bind', '/', '/',
           '--bind', str(out), str(out), '--tmpfs', '/tmp', '--ro-bind', str(root), str(root), '--proc', '/proc', '--dev', '/dev',
           '--chdir', str(ROOT), '--clearenv', '--setenv', 'HOME', '/tmp',
           '--setenv', 'GOMAXPROCS', str(args.gomaxprocs), '--setenv', 'GOGC', args.gogc,
           '--setenv', 'GOMEMLIMIT', args.gomemlimit, *gpu_flags, '--', str(out / 'proverbench'),
           '--artifacts', str(root), '--backend', args.backend, '--scenario', args.scenario,
           '--sample', args.sample, '--cycles', str(args.cycles), '--warmup', str(args.warmup),
           '--interval', f'{args.interval}s', '--sample-interval', f'{args.sample_interval}s']
    if gpu:
        cmd += ['--device', '0', '--msm-chunk-size', str(args.msm_chunk_size),
                '--msm-internal-chunks', str(args.msm_internal_chunks),
                '--backend-dir', str(Path(build_info['library_dir']) / 'backend')]
    if args.gc_between:
        cmd.append('--gc-between')
    if args.profile:
        cmd += ['--cpu-profile', str(out / 'cpu.pprof'), '--heap-profile', str(out / 'heap.pprof')]
    events, stop, thread = [], threading.Event(), None
    gpu_thread = None
    with (out / 'metrics.jsonl').open('x') as f:
        p = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        try:
            for line in p.stderr:
                try:
                    e = json.loads(line)
                    if not isinstance(e, dict) or 'event' not in e or 'context' not in e:
                        raise ValueError('not a metric')
                except (json.JSONDecodeError, ValueError):
                    # Do not persist arbitrary stderr (may contain library inputs).
                    e = {'event': 'error', 'context': {}, 'code': 'non_metric_stderr_redacted'}
                events.append(e); f.write(json.dumps(e) + '\n'); f.flush()
                if e['event'] == 'config' and args.sample_interval > 0 and thread is None:
                    thread = threading.Thread(target=observe, args=(e['pid'], stop, out / 'external.jsonl', args.sample_interval), daemon=True)
                    thread.start()
                if e['event'] == 'config' and gpu and args.gpu_sample_interval > 0 and gpu_thread is None:
                    gpu_thread = threading.Thread(target=gpu_runtime.observe,
                        args=(card['uuid'], e['pid'], stop, out / 'gpu.jsonl', args.gpu_sample_interval), daemon=True)
                    gpu_thread.start()
                if e['event'] in ('proof', 'error'):
                    print(json.dumps(e), flush=True)
            code = p.wait()
        finally:
            if p.poll() is None:
                p.terminate()
                try: p.wait(timeout=10)
                except subprocess.TimeoutExpired: p.kill(); p.wait()
            stop.set()
            if thread: thread.join(timeout=5)
            if gpu_thread: gpu_thread.join(timeout=12)
            write_json(out / 'exit.json', {'exit_code': p.returncode})
    summary = summarize(events, info, code)
    if (out / 'gpu.jsonl').exists():
        summary['gpu'] = gpu_runtime.summarize(out / 'gpu.jsonl')
    write_json(out / 'summary.json', summary)
    print(json.dumps({'run': str(out), 'complete': summary['complete'], 'verified_measured': summary['verified_measured']}))
    return 0 if summary['complete'] else 1


def report(path):
    root = Path(path)
    info = json.loads((root / 'run.json').read_text())
    events = [json.loads(line) for line in (root / 'metrics.jsonl').read_text().splitlines()]
    code = json.loads((root / 'exit.json').read_text())['exit_code'] if (root / 'exit.json').exists() else None
    summary = summarize(events, info, code)
    if (root / 'gpu.jsonl').exists():
        import gpu_runtime
        summary['gpu'] = gpu_runtime.summarize(root / 'gpu.jsonl')
    return summary


def compare(paths):
    rows = []
    for path in paths:
        p = Path(path); meta = json.loads((p / 'run.json').read_text()); summary = report(p)
        rows.append((p, meta, summary))
    base = rows[0][1]
    mismatches = []
    for p, m, s in rows:
        if not s['complete']: mismatches.append(f'{p.name}: incomplete')
        if s.get('gpu', {}).get('other_compute_processes_observed'):
            mismatches.append(f'{p.name}: other compute processes observed on selected GPU')
        if m.get('gpu') and base.get('gpu'):
            for key in ('uuid', 'driver_version', 'compute_cap'):
                if m['gpu'].get(key) != base['gpu'].get(key):
                    mismatches.append(f'{p.name}: different GPU {key}')
            for key in ('msm_chunk_size', 'msm_internal_chunks', 'gpu_sample_interval'):
                if m['config'].get(key) != base['config'].get(key):
                    mismatches.append(f'{p.name}: different {key}')
        for key in ('input_sha256', 'samples', 'host', 'kernel', 'cpu_model', 'affinity'):
            if m[key] != base[key]: mismatches.append(f'{p.name}: different {key}')
        for key in ('go_version', 'go_binary_sha256', 'cgo_enabled'):
            if m['build'][key] != base['build'][key]: mismatches.append(f'{p.name}: different {key}')
        for key in ('cycles', 'warmup', 'interval', 'sample_interval', 'gomaxprocs', 'gogc', 'gomemlimit', 'gc_between', 'profile'):
            if m['config'][key] != base['config'][key]: mismatches.append(f'{p.name}: different {key}')
    print(json.dumps({'comparison_warnings': mismatches,
                      'runs': [{'name': p.name, 'backend': m['config']['backend'], 'summary': s} for p,m,s in rows]}, indent=2))


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    b = sub.add_parser('build', help='compile only; never execute benchmark')
    b.add_argument('--go', default=str(DEFAULT_GO)); b.add_argument('--modcache', default=str(Path.home() / 'go/pkg/mod'))
    b.add_argument('--compile-tests', action='store_true', help='compile test executable with go test -c; do not run it')
    r = sub.add_parser('run', help='explicitly execute proof workload')
    r.add_argument('--name', required=True); r.add_argument('--artifacts', default=str(DEFAULT_DATA))
    r.add_argument('--backend', default='cpu', choices=['cpu', 'cpu-msm', 'icicle-msm']); r.add_argument('--scenario', default='all', choices=['all', *GROUPS])
    r.add_argument('--sample', default=''); r.add_argument('--cycles', type=int, default=1)
    r.add_argument('--warmup', type=int, default=1); r.add_argument('--interval', type=float, default=0)
    r.add_argument('--sample-interval', type=float, default=1); r.add_argument('--gomaxprocs', type=int, default=8)
    r.add_argument('--gogc', default='100'); r.add_argument('--gomemlimit', default='off')
    r.add_argument('--gpu-uuid', default='', help='full physical GPU UUID; required for icicle-msm')
    r.add_argument('--cpu-control', action='store_true', help='run CPU MSM using the exact CUDA-linked binary, without GPU device mapping')
    r.add_argument('--msm-internal-chunks', type=int, default=4, choices=[1,2,4,8])
    r.add_argument('--msm-chunk-size', type=int, default=1 << 20)
    r.add_argument('--gpu-sample-interval', type=float, default=2)
    r.add_argument('--gc-between', action='store_true'); r.add_argument('--profile', action='store_true')
    p = sub.add_parser('report'); p.add_argument('path')
    c = sub.add_parser('compare'); c.add_argument('paths', nargs='+')
    args = parser.parse_args()
    try:
        if args.command == 'build': build(args)
        elif args.command == 'run': return run(args)
        elif args.command == 'report': print(json.dumps(report(args.path), indent=2))
        else: compare(args.paths)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as e:
        print(f'lab: {e}', file=sys.stderr); return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
