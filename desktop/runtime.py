"""Build the pinned Neutralino runtime with Snoot's Cocoa lifecycle repair."""
import hashlib
import os
import pathlib
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

VERSION = '6.10.0'
SOURCE_SHA256 = 'fa8397f7ed8bb3fe877245ed7a2063c8b03cc6234e9c40aa8cf177fbe9051bda'


def build_runtime(root, arch):
    cache = root / 'desktop/cache'
    cache.mkdir(parents=True, exist_ok=True)
    patch = root / 'desktop/neutralino-macos-exit.patch'
    key = hashlib.sha256(patch.read_bytes()).hexdigest()[:16]
    binary = cache / f'neutralino-{VERSION}-{key}-{arch}'
    if binary.exists():
        return binary
    archive = cache / f'neutralinojs-source-v{VERSION}.tar.gz'
    if not archive.exists():
        urllib.request.urlretrieve(
            f'https://github.com/neutralinojs/neutralinojs/archive/refs/tags/v{VERSION}.tar.gz',
            archive)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != SOURCE_SHA256:
        raise RuntimeError('Neutralino source checksum mismatch')
    with tempfile.TemporaryDirectory(prefix='snoot-neutralino-') as temporary:
        stage = pathlib.Path(temporary)
        with tarfile.open(archive) as source:
            source.extractall(stage, filter='data')
        source = stage / f'neutralinojs-{VERSION}'
        subprocess.run(['patch', '-p1', '-i', str(patch)], cwd=source, check=True)
        architectures = {'arm64': 'arm64', 'x64': 'x86_64', 'universal': 'arm64;x86_64'}
        subprocess.run([
            'cmake', '-S', str(source), '-B', str(stage / 'build'),
            '-DCMAKE_BUILD_TYPE=Release', '-DCMAKE_OSX_DEPLOYMENT_TARGET=14.0',
            f'-DCMAKE_OSX_ARCHITECTURES={architectures[arch]}',
        ], check=True)
        subprocess.run(['cmake', '--build', str(stage / 'build'), '--parallel',
                        str(min(os.cpu_count() or 2, 8))], check=True)
        outputs = list((source / 'bin').glob('neutralino-mac_*'))
        if len(outputs) != 1:
            raise RuntimeError('Expected one compiled Neutralino runtime')
        shutil.copy2(outputs[0], binary)
    return binary
