#!/usr/bin/env python3
"""Run the real OPFS publication code in a native WebKit worker, including faults."""
import argparse
import json
from pathlib import Path
import socket
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('app', type=Path)
    parser.add_argument('--endpoint', default='http://127.0.0.1:3020')
    args = parser.parse_args()
    app = args.app.resolve()
    with tempfile.TemporaryDirectory(prefix='snoot-storage-test-') as temporary:
        root = Path(temporary)
        resources = root / 'resources'
        web = resources / 'web'
        web.mkdir(parents=True)
        config = json.loads((app / 'Contents/Resources/neutralino.config.json').read_text())
        config.update(dataLocation='app', storageLocation='app', applicationId='com.authenticitylabs.snoot.storage-test')
        (resources / 'neutralino.config.json').write_text(json.dumps(config))
        script = "import {build} from 'rolldown'; await build({input:process.argv[1],output:{file:process.argv[2],format:'esm'}});"
        subprocess.run(['node', '--input-type=module', '-e', script,
                        str(Path(__file__).with_name('storage.worker.ts')), str(web / 'storage.worker.js')],
                       cwd=ROOT / 'client', check=True)
        (web / 'index.html').write_text('<!doctype html><head><script defer src="/storage-test.js"></script></head><body>Storage Test</body>')
        (web / 'storage-test.js').write_text('''
const worker=new Worker('/storage.worker.js',{type:'module'});
const report=async data=>{worker.terminate();await fetch('/__desktop/report',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)})};
worker.onmessage=({data})=>report(data);
worker.onerror=e=>report({ok:false,error:e.message});
worker.postMessage({});
''')
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        process = subprocess.run([str(app / 'Contents/MacOS/snoot'), '--self-test',
                                  '--endpoint', args.endpoint, '--transport', 'http', '--port', str(port),
                                  '--state-dir', str(root / 'profile'), '--resources', str(resources)],
                                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=45)
        report = root / 'profile/self-test.json'
        if not report.exists():
            raise SystemExit('Native storage test produced no report')
        result = json.loads(report.read_text())
        print(json.dumps(result, indent=2))
        if process.returncode or not result.get('ok'):
            raise SystemExit('Native storage test failed')


if __name__ == '__main__':
    main()
