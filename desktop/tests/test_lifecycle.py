#!/usr/bin/env python3
"""Exercise the packaged Cocoa runtime and bridge with isolated profile storage."""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('app', type=Path)
    args = parser.parse_args()
    app = args.app.resolve()
    reports = Path.home() / 'Library/Logs/DiagnosticReports'
    before = set(reports.glob('neutralino-*.ips'))
    with tempfile.TemporaryDirectory(prefix='snoot-lifecycle-') as tmp:
        root = Path(tmp)
        dylib = root / 'lifecycle.dylib'
        subprocess.run(['clang', '-dynamiclib', '-framework', 'Cocoa',
                        str(Path(__file__).with_name('lifecycle.m')), '-o', str(dylib)], check=True)
        resources = root / 'resources'
        resources.mkdir()
        shutil.copytree(app / 'Contents/Resources/web', resources / 'web')
        config = json.loads((app / 'Contents/Resources/neutralino.config.json').read_text())
        config['dataLocation'] = 'app'
        config['storageLocation'] = 'app'
        config['applicationId'] = 'com.authenticitylabs.snoot.lifecycle-test'
        (resources / 'neutralino.config.json').write_text(json.dumps(config))
        # Reuse the same port: every exit must release it before the next launch.
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        for action in ['close-button', 'close-command', 'quit', 'repeated-close', 'signal', 'parent-term', 'close-key', 'quit-key']:
            saved = resources / '.tmp/window_state.config.json'
            pid_file = root / 'runtime.pid'
            if saved.exists(): saved.unlink()
            if pid_file.exists(): pid_file.unlink()
            process = subprocess.Popen([
                str(app / 'Contents/MacOS/snoot'), '--resources', str(resources),
                '--state-dir', str(root / 'profile'), '--port', str(port),
                '--endpoint', 'http://127.0.0.1:1', '--transport', 'http',
            ], env=dict(os.environ, DYLD_INSERT_LIBRARIES=str(dylib), SNOOT_TEST_CLOSE=action,
                        SNOOT_TEST_PID_FILE=str(pid_file)),
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
            try:
                _, stderr = process.communicate(timeout=20)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.communicate()
                raise AssertionError(f'{action}: shutdown timed out')
            if process.returncode != 0 or f'TEST: {action}' not in stderr:
                raise AssertionError(f'{action}: exit={process.returncode}\n{stderr}')
            # Neutralino saves a nonempty state file in .tmp on every close path.
            state = json.loads(saved.read_text())
            if state['width'] <= 0 or state['height'] <= 0:
                raise AssertionError(f'{action}: invalid saved window state')
            try:
                os.kill(int(pid_file.read_text()), 0)
            except ProcessLookupError:
                pass
            else:
                raise AssertionError(f'{action}: orphan runtime is still running')
            with socket.socket() as listener:
                # Match Go's listener: closed requests can still be in TIME_WAIT.
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(('127.0.0.1', port))
            print(f'{action}: clean exit, saved window state, bridge port released', flush=True)
    after = set(reports.glob('neutralino-*.ips'))
    if after - before: raise AssertionError(f'Native crash reports created: {after - before}')


if __name__ == '__main__':
    main()
