"""Bounded allocation experiment; no production files. Sparse seek vs written zeros."""
import json, os, platform, tempfile, time, pathlib, fcntl, struct
root=pathlib.Path(__file__).resolve().parent
cases=json.loads((root/'cases.json').read_text())
def payload(c,slot):
    n=c['payload']-slot%17
    return n.to_bytes(4,'little')+bytes((slot*17+i*29)%256 for i in range(n))
def entries(c):
    if 'objects' not in c:
        return [(slot*c['stride'],payload(c,slot)) for start,count in c['groups'] for slot in range(start,start+count)]
    out=[]
    for obj in reversed(range(c['objects'])):
        base=obj*c['stride'];out.append((base,(obj+1).to_bytes(16,'big')))
        for rev in c.get('revisions',range(c.get('revisionCount',0))):
            assert 0<=rev<=65535
            out.append((base+64+rev*8,(obj+1).to_bytes(4,'little')+(rev+1).to_bytes(4,'little')))
    return out
def snapshot(fd):
    s=os.fstat(fd)
    return dict(logical=s.st_size,allocated=s.st_blocks*512)
results=[]
with tempfile.TemporaryDirectory(prefix='trunk-fixed-slot-',dir=root) as directory:
 for c in cases:
    path=os.path.join(directory,c['name']); fd=os.open(path,os.O_RDWR|os.O_CREAT,0o600)
    start=time.perf_counter_ns()
    if c.get('zero'):
        b=bytes(1<<20)
        for offset in range(0,c['slots']*c['stride'],len(b)):os.pwrite(fd,b,offset)
    elif not c.get('seek'): os.ftruncate(fd,c['slots']*c['stride'])
    os.fsync(fd); empty=snapshot(fd)
    used=entries(c)
    for at,b in used:assert os.pwrite(fd,b,at)==len(b)
    os.fsync(fd); written=snapshot(fd);ms=(time.perf_counter_ns()-start)/1e6
    for at,b in used: assert os.pread(fd,len(b),at)==b
    if 'objects' in c:
        for obj in range(c['objects']):assert os.pread(fd,48,obj*c['stride']+16)==bytes(48)
    os.close(fd);fd=os.open(path,os.O_RDWR)
    for at,b in used:assert os.pread(fd,len(b),at)==b
    start=time.perf_counter_ns()
    for i in range(2000):
        at,b=used[(i*997)%len(used)];assert os.pread(fd,4,at)==b[:4]
    read_ns=(time.perf_counter_ns()-start)/2000
    punched=None
    if platform.system()=='Darwin' and not c.get('zero'):
        # macOS SDK sys/fcntl.h: F_PUNCHHOLE=99, fpunchhole_t=(uint,uint,off_t,off_t).
        # Only full 4KiB pages that contain no written bytes are deallocated.
        begin=time.perf_counter_ns();previous=0;calls=0
        try:
            for at,b in sorted(used):
                start=(previous+4095)//4096*4096;end=at//4096*4096
                if end>start:fcntl.fcntl(fd,99,struct.pack('=IIqq',0,0,start,end-start));calls+=1
                previous=max(previous,at+len(b))
            os.fsync(fd);punched=dict(**snapshot(fd),calls=calls,ms=(time.perf_counter_ns()-begin)/1e6)
            for at,b in used:assert os.pread(fd,len(b),at)==b
        except OSError as e:punched=dict(error=str(e))
    # Whole-slot tail truncation reclaims allocated extents without moving earlier slots.
    os.ftruncate(fd,c['stride']*max(1,c['slots']//2));os.fsync(fd);shrunk=snapshot(fd)
    os.close(fd);os.unlink(path)
    r=dict(case=c,empty=empty,written=written,punched=punched,shrunk=shrunk,write_flush_ms=ms,
           records=len(used),data_bytes=sum(len(b) for _,b in used),reopen_verified=True,
           header_read_python_ns=read_ns)
    results.append(r); print(json.dumps(r),flush=True)
(root/'server.json').write_text(json.dumps(dict(platform=platform.platform(),results=results),indent=2)+'\n')
