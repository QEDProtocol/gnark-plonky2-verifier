#!/usr/bin/env python3
"""Prepare pinned experimental dependencies and compile; never run proofs/tests."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

import lab

ROOT = lab.ROOT
HERE = Path(__file__).resolve().parent
DEPS = ROOT / 'lab-deps'


def tree_digest(root):
    h = hashlib.sha256()
    for p in sorted(root.rglob('*')):
        rel = p.relative_to(root)
        if '.git' in rel.parts:
            continue
        if p.is_symlink():
            raise ValueError(f'dependency symlink not allowed: {rel}')
        if p.is_file():
            h.update(rel.as_posix().encode() + b'\0' + lab.digest(p).encode() + b'\n')
    return h.hexdigest()


def verify_dependencies(gpu):
    pin = json.loads((HERE / 'gnark-source.json').read_text())
    hashes = {}
    for name in (['gnark', 'icicle-gnark'] if gpu else ['gnark']):
        root = DEPS / name
        if not root.is_dir():
            raise ValueError('missing dependencies; run gpu_build.py prepare first')
        hashes[name] = tree_digest(root)
        if hashes[name] != pin[name + '_patched_sha256']:
            raise ValueError(f'{name} differs from reviewed patch; do not silently rebuild changed dependency')
    if gpu:
        head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=DEPS / 'icicle-gnark', text=True).strip()
        if head != pin['icicle_revision']:
            raise ValueError('unexpected ICICLE revision')
    return hashes


def prepare(args):
    pin = json.loads((HERE / 'gnark-source.json').read_text())
    DEPS.mkdir(exist_ok=True)
    gnark = DEPS / 'gnark'
    if not gnark.exists():
        src = Path(args.modcache) / (pin['module'] + '@' + pin['version'])
        if tree_digest(src) != pin['gnark_original_sha256']:
            raise ValueError('original gnark module mismatch or missing; no automatic download')
        shutil.copytree(src, gnark)
        gnark.chmod(0o755)
        for p in gnark.rglob('*'):
            p.chmod(0o755 if p.is_dir() else 0o644)
        subprocess.run(['patch', '--batch', '-p1', '-i', str(HERE / 'gnark-msm.patch')], cwd=gnark, check=True)
        subprocess.run(['patch', '--batch', '-p1', '-i', str(HERE / 'gnark-h-timing.patch')], cwd=gnark, check=True)
    if args.gpu and not (DEPS / 'icicle-gnark').exists():
        if not args.fetch:
            raise ValueError('ICICLE missing; use prepare --gpu --fetch to explicitly download pinned source')
        target = DEPS / 'icicle-gnark'
        subprocess.run(['git', 'clone', '--depth', '1', '--branch', pin['icicle_tag'],
                        'https://github.com/ingonyama-zk/icicle-gnark.git', str(target)], check=True)
        head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=target, text=True).strip()
        if head != pin['icicle_revision']:
            raise ValueError('ICICLE tag no longer matches pinned commit')
        subprocess.run(['patch', '--batch', '-p1', '-i', str(HERE / 'icicle-runtime.patch')], cwd=target, check=True)
        subprocess.run(['patch', '--batch', '-p1', '-i', str(HERE / 'icicle-cuda-build.patch')], cwd=target, check=True)
        subprocess.run(['patch', '--batch', '-p1', '-i', str(HERE / 'icicle-msm-stream-dependency.patch')], cwd=target, check=True)
        subprocess.run(['patch', '--batch', '-p1', '-i', str(HERE / 'icicle-msm-chunk-cleanup.patch')], cwd=target, check=True)
    print(json.dumps({'dependencies': verify_dependencies(args.gpu), 'workload_executed': False}))


def build(args):
    gpu = args.variant == 'icicle-msm'
    cuda = args.cuda
    if cuda and not gpu:
        raise ValueError('--cuda requires --variant icicle-msm')
    deps = verify_dependencies(gpu)
    go = Path(args.go).resolve()
    version = subprocess.check_output([str(go), 'version'], text=True).strip()
    if ' go1.22.3 ' not in version:
        raise ValueError('requires pinned Go 1.22.3')
    cache = ROOT / 'lab-cache'
    out = ROOT / 'lab-bin' / ('icicle-cuda' if cuda else args.variant)
    cache.mkdir(exist_ok=True)
    out.mkdir(parents=True, exist_ok=True)
    env = {k: os.environ[k] for k in ('HOME', 'PATH', 'TMPDIR') if k in os.environ}
    env.update(GOTOOLCHAIN='local', GOENV='off', GOPROXY='off', GOSUMDB='off',
               GOCACHE=str(cache), GOMODCACHE=str(Path(args.modcache).resolve()),
               GOMAXPROCS='1', CGO_ENABLED='1' if gpu else '0',
               CCACHE_DIR=str(cache / 'ccache'))
    libs = {}
    cuda_meta = {}
    if gpu:
        # Explicit architecture avoids native GPU discovery during compilation.
        suffix = 'cuda-sm' + args.cuda_arch if cuda else 'dispatch'
        prefix = DEPS / ('icicle-' + suffix)
        cmake_build = cache / ('icicle-' + suffix + '-build')
        cc, cxx = shutil.which('clang'), shutil.which('clang++')
        if not cc or not cxx:
            raise ValueError('clang and clang++ required for a stable ICICLE CMake cache')
        cmake = ['cmake', '-S', str(DEPS / 'icicle-gnark/icicle'), '-B', str(cmake_build),
                 '-DCMAKE_C_COMPILER=' + cc, '-DCMAKE_CXX_COMPILER=' + cxx,
                 '-DCURVE=bn254', '-DCPU_BACKEND=OFF', '-DCUDA_BACKEND=' + ('ON' if cuda else 'OFF'),
                 '-DBUILD_TESTS=OFF', '-DMSM=ON', '-DG2=ON', '-DNTT=ON',
                 '-DCMAKE_INSTALL_PREFIX=' + str(prefix)]
        if cuda:
            nvcc = Path(args.nvcc).resolve()
            host = Path(args.cuda_host_compiler).resolve()
            if not nvcc.is_file() or not host.is_file():
                raise ValueError('missing CUDA compiler or host compiler')
            env['PATH'] = str(nvcc.parent) + os.pathsep + env.get('PATH', '')
            cmake += ['-DCMAKE_CUDA_COMPILER=' + str(nvcc),
                      '-DCMAKE_CUDA_HOST_COMPILER=' + str(host),
                      '-DCUDA_ARCH=' + args.cuda_arch,
                      '-DCMAKE_CUDA_ARCHITECTURES=' + args.cuda_arch]
            cuda_meta = {'nvcc_version': subprocess.check_output([str(nvcc), '--version'], text=True),
                         'nvcc_sha256': lab.digest(nvcc), 'architecture': args.cuda_arch,
                         'host_compiler': str(host), 'host_compiler_sha256': lab.digest(host),
                         'toolkit_libdir': str(nvcc.parent.parent / 'lib64')}
        subprocess.run(['nice', '-n', '19', *cmake], cwd=ROOT, env=env, check=True)
        # Upstream sets compilers after project(), which can reset cached flags.
        # Fail before build/install if CMake ever discarded the isolation flags.
        values = {}
        for line in (cmake_build / 'CMakeCache.txt').read_text().splitlines():
            if line.startswith(('#', '//')) or '=' not in line:
                continue
            key, value = line.split('=', 1)
            values[key.split(':')[0]] = value
        required = {'CMAKE_INSTALL_PREFIX': str(prefix), 'CPU_BACKEND': 'OFF',
                    'CUDA_BACKEND': 'ON' if cuda else 'OFF', 'BUILD_TESTS': 'OFF', 'CURVE': 'bn254'}
        if cuda:
            required.update(CUDA_ARCH=args.cuda_arch, CMAKE_CUDA_ARCHITECTURES=args.cuda_arch)
        if any(values.get(k) != v for k, v in required.items()):
            raise ValueError('CMake changed required compile-only settings; refusing build/install')
        for cmd in (['cmake', '--build', str(cmake_build), '--parallel', '1'],
                    ['cmake', '--install', str(cmake_build)]):
            subprocess.run(['nice', '-n', '19', *cmd], cwd=ROOT, env=env, check=True)
        libdir = prefix / 'lib'
        # Isolated prefix; upstream bindings additionally contain /usr/local/lib.
        env['CGO_LDFLAGS'] = f'-L{libdir} -Wl,-rpath,{libdir}'
        libs = {p.relative_to(libdir).as_posix(): lab.digest(p) for p in sorted(libdir.rglob('*.so'))}
        if not {'libicicle_device.so', 'libicicle_curve_bn254.so', 'libicicle_field_bn254.so'} <= libs.keys():
            raise ValueError('dispatch library installation incomplete')
        if cuda and not {'backend/cuda/libicicle_backend_cuda_device.so',
                         'backend/bn254/cuda/libicicle_backend_cuda_field_bn254.so',
                         'backend/bn254/cuda/libicicle_backend_cuda_curve_bn254.so'} <= libs.keys():
            raise ValueError('CUDA backend library installation incomplete')
    mod = 'lab-gpu.mod' if gpu else 'lab-msm.mod'
    tags = 'proverbench,msmhook' + (',iciclemsm' if gpu else '')
    common = ['-p', '1', '-mod=readonly', '-modfile=' + mod, '-trimpath', '-tags=' + tags]
    cmd = [str(go), 'build', *common, '-o', str(out / 'proverbench'), './cmd/proverbench']
    subprocess.run(['nice', '-n', '19', *cmd], cwd=ROOT, env=env, check=True)
    if args.compile_tests:
        subprocess.run(['nice', '-n', '19', str(go), 'test', '-c', *common,
                        '-o', str(out / 'proverbench.test'), './cmd/proverbench'], cwd=ROOT, env=env, check=True)
    meta = {'backend': args.variant, 'base_commit': subprocess.check_output(
                ['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
            'source_sha256': lab.source_digest(), 'dependency_sha256': deps,
            'binary_sha256': lab.digest(out / 'proverbench'), 'go_version': version,
            'test_binary_sha256': lab.digest(out / 'proverbench.test') if args.compile_tests else None,
            'go_binary_sha256': lab.digest(go), 'build_command': cmd,
            'production_binary': False, 'cgo_enabled': gpu,
            'dispatch_library_sha256': libs, 'cuda_backend_built': cuda,
            'library_dir': str(prefix / 'lib') if gpu else None, 'cuda': cuda_meta,
            'runtime_verified': False, 'tests_executed': False}
    (out / 'build.json').write_text(json.dumps(meta, indent=2) + '\n')
    print(json.dumps({'built': str(out / 'proverbench'), 'workload_executed': False,
                      'cuda_backend_built': cuda}))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest='command', required=True)
    prep = sub.add_parser('prepare')
    prep.add_argument('--gpu', action='store_true')
    prep.add_argument('--fetch', action='store_true')
    b = sub.add_parser('build')
    b.add_argument('--variant', required=True, choices=['cpu-msm', 'icicle-msm'])
    b.add_argument('--go', default=str(lab.DEFAULT_GO))
    b.add_argument('--compile-tests', action='store_true')
    b.add_argument('--cuda', action='store_true', help='compile CUDA kernels; does not run GPU code')
    b.add_argument('--cuda-arch', default='120', choices=['120'])
    b.add_argument('--nvcc', default='/opt/cuda/bin/nvcc')
    b.add_argument('--cuda-host-compiler', default='/usr/bin/g++-14')
    for parser in (prep, b):
        parser.add_argument('--modcache', default=str(Path.home() / 'go/pkg/mod'))
    args = p.parse_args()
    if args.command == 'prepare':
        prepare(args)
    else:
        build(args)


if __name__ == '__main__':
    main()
