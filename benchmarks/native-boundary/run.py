#!/usr/bin/env python3
"""Run each case in a fresh process, serially. Build first with build.sh."""
import argparse, hashlib, json, pathlib, platform, subprocess
p=argparse.ArgumentParser()
p.add_argument('--output',required=True)
p.add_argument('--repeats',type=int,default=3)
a=p.parse_args()
root=pathlib.Path(__file__).resolve().parent
out=pathlib.Path(a.output);out.parent.mkdir(parents=True,exist_ok=True)
def command(*args):
    return subprocess.check_output(args,cwd=root,text=True).strip()
metadata={
    'platform':platform.platform(), 'machine':platform.machine(),
    'go':command('go','version'), 'rust':command('rustc','--version'),
    'head':command('git','rev-parse','HEAD'),
    'dirty':bool(command('git','status','--porcelain')),
    'repeats':a.repeats,'chunk_bytes':64<<10,
    'source_sha256':{p.name:hashlib.sha256(p.read_bytes()).hexdigest()
                     for p in sorted(root.iterdir()) if p.suffix in ('.go','.rs')},
}
out.with_suffix('.environment.json').write_text(json.dumps(metadata,indent=2)+'\n')
with out.open('w') as f:
    for tls in [False,True]:
        for size,streams in [(8<<20,1),(64<<20,1),(8<<20,8)]:
            for repeat in range(a.repeats):
                # Rotate order to reduce systematic thermal/order bias.
                modes=['go','duplicate','copy','direct']
                modes=modes[repeat%4:]+modes[:repeat%4]
                for mode in modes:
                    cmd=[str(root/'build/boundary-probe'),'-mode',mode,'-bytes',str(size),'-streams',str(streams),'-chunk',str(64<<10)]
                    if tls:cmd+=['-tls']
                    sample=json.loads(subprocess.check_output(cmd,text=True,timeout=180))
                    sample['repeat']=repeat
                    f.write(json.dumps(sample)+'\n');f.flush()
print(out)
