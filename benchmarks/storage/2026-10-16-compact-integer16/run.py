"""Sequential, paired I16/U16 mask-only shortening experiments."""
import hashlib,json,os,subprocess,tarfile
from pathlib import Path
from datetime import datetime,timezone
ROOT=Path(__file__).resolve().parent
REPO=ROOT.parents[2]
ENV={k:v for k,v in os.environ.items() if not k.startswith(('TRUNK_MEASURE_','TRUNK_INT16_'))}|{'GOCACHE':'/private/tmp/trie-trunk-go-cache'}
CASES=[(100000,f'{kind}-{p}',shape) for kind in ('u16','i16') for p in ('0','25','75','100','uniform') for shape in ('dense8','dense16','sparse1','sparse2')]
CASES += [(1000000,profile,shape) for profile in ('mixed-unsigned','mixed-signed') for shape in ('full','sparse')]
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def main():
 binary=ROOT/'cache.test'
 subprocess.run(['go','-C',str(REPO/'go'),'test','-c','-o',str(binary),'./server/cache'],env=ENV,check=True)
 sources=sorted((REPO/'go').rglob('*.go'))+[REPO/'go/go.mod',REPO/'go/go.sum']
 hashes={str(p.relative_to(REPO)):digest(p) for p in sources}
 archive=ROOT/'source.tar.gz'
 with tarfile.open(archive,'w:gz') as tar:
  for p in sources:tar.add(p,arcname=p.relative_to(REPO))
 meta={'started_utc':datetime.now(timezone.utc).isoformat(),'go':subprocess.check_output(['go','version'],env=ENV,text=True).strip(),'source_sha256':hashes,'archive_sha256':digest(archive),'executable_sha256':digest(binary),'commands':[],'integer16_generator':'PCG seeds 0x70696c16,0x63626f7216; independent shrinkability draws; uniform values within fitting or non-fitting subsets'}
 for n,profile,shape in CASES:
  target=ROOT/'results'/profile/f'{shape}-{n}.json';target.parent.mkdir(parents=True,exist_ok=True)
  env=ENV|{'TRUNK_MEASURE_EVENTS':str(n),'TRUNK_INT16_PROFILE':profile,'TRUNK_MEASURE_SHAPE':shape,'TRUNK_MEASURE_OUT':str(target)}
  print(f'Measuring {profile} {shape} {n:,}',flush=True)
  with target.with_suffix('.log').open('w') as log:
   subprocess.run([str(binary),'-test.run','^TestMeasureInteger16$','-test.timeout','10m'],cwd=REPO,env=env,stdout=log,stderr=subprocess.STDOUT,check=True)
  meta['commands'].append({'events':n,'profile':profile,'shape':shape,'output':str(target.relative_to(ROOT))})
  (ROOT/'metadata.json').write_text(json.dumps(meta,indent=2)+'\n')
 assert hashes=={str(p.relative_to(REPO)):digest(p) for p in sources},'measured source changed'
 meta['finished_utc']=datetime.now(timezone.utc).isoformat();(ROOT/'metadata.json').write_text(json.dumps(meta,indent=2)+'\n')
 print('All 44 cases completed.',flush=True)
if __name__=='__main__':main()
