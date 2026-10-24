#!/usr/bin/env python3
"""Instrument the real store's accepted batch immediately before swap.Sync.
Only the benchmark binary sees this source; durable acknowledgements stay intact.
"""
import json,pathlib,sys
repo,out=map(pathlib.Path,sys.argv[1:])
p=repo/'go/server/cache/store.go'
s=p.read_text()
needle='\tif b := s.branches[branch]; b != nil && b.swap != nil {\n\t\tif e := b.swap.Sync(); e != nil {'
assert s.count(needle)==1
s=s.replace(needle,'''\tif count>0 && BenchmarkBeforeSwapSync!=nil {
  if e:=BenchmarkBeforeSwapSync(frames[:count]);e!=nil{return count,e}
 }
'''+needle)
s+='\n// Benchmark-only observer; installed before requests and removed after completion.\nvar BenchmarkBeforeSwapSync func([][]byte) error\n'
patch=out/'staged_store.go';patch.write_text(s)
(out/'overlay.json').write_text(json.dumps({'Replace':{str(p.resolve()):str(patch.resolve())}}))
