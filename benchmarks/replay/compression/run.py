#!/usr/bin/env python3
"""Native gzip/Brotli control using native PIL fixtures only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

here=Path(__file__).resolve().parent
parser=argparse.ArgumentParser()
parser.add_argument('output')
parser.add_argument('--format', choices=['pil'], default='pil')
parser.add_argument('--context', choices=['stream','response'], default='stream')
args=parser.parse_args()
out=Path(args.output).resolve()
out.mkdir(parents=True,exist_ok=True)
repo=here.parents[2]
prefix=Path(os.environ.get('BROTLI_PREFIX','/opt/homebrew'))
with tempfile.TemporaryDirectory(prefix='trie-compression-') as tmp:
    tmp=Path(tmp);fixtures=tmp/'fixtures';binary=tmp/'stream'
    subprocess.run(['go','run','.', '-compression-fixtures',str(fixtures),'-compression-format',args.format],cwd=here.parent/'postgres',check=True)
    subprocess.run(['clang','-O3','-Wall','-Wextra','-Werror','-I'+str(prefix/'include'),str(here/'stream.c'),'-L'+str(prefix/'lib'),'-lbrotlienc','-lbrotlidec','-lbrotlicommon','-lz','-o',str(binary)],check=True)
    results=[];manifest={};versions=''
    for file in sorted(fixtures.glob('*.bin')):
        result=subprocess.run([str(binary),str(file)]+(['response'] if args.context=='response' else []),capture_output=True,text=True,check=True)
        manifest[file.name]=hashlib.sha256(file.read_bytes()).hexdigest();versions=result.stderr.strip()
        variant,sz,ob=file.stem.rsplit('_',2)
        for line in result.stdout.splitlines():
            row=json.loads(line);row.update(context=args.context,variant=variant,description_bytes=int(sz[1:]),objects=int(ob[1:]));results.append(row)
        print(file.name,'done',flush=True)
    assert len(results)==200
    (out/'format.txt').write_text(args.format+'\n')
    (out/'context.txt').write_text(args.context+'\n')
    (out/'results.json').write_text(json.dumps(results,indent=2)+'\n')
    (out/'fixtures.sha256.json').write_text(json.dumps(manifest,indent=2)+'\n')
    (out/'versions.txt').write_text(versions+'\nclock=CLOCK_MONOTONIC_RAW\n'+subprocess.check_output(['clang','--version'],text=True))
    hashes={str(p.relative_to(repo)):hashlib.sha256(p.read_bytes()).hexdigest() for p in [here/'stream.c',here.parent/'postgres'/'compression_fixture.go',here.parent/'postgres'/'main.go',here.parent/'postgres'/'stream.go']}
    hashes['benchmark_binary']=hashlib.sha256(binary.read_bytes()).hexdigest()
    (out/'sources.sha256.json').write_text(json.dumps(hashes,indent=2)+'\n')
