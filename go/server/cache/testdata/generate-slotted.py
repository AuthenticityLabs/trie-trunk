"""Independent version-zero fixture: U24 bases, 5-byte refs, rotated slots.
Run from this directory with Python 3. Does not call the production codec.
"""
import json
from pathlib import Path
import struct

p = bytearray(4096)
p[:4] = b'OBJ0'
p[29:32] = bytes([1, 2, 3])
p[37:160] = bytes([128])*123
queries = []
used = 0
# Deliberately scatter physical units while preserving logical UUID order.
units = list(range(5, 128, 2)) + list(range(6, 128, 2))
for key, base, span, cursor in [(0, 65536, 14, 5), (2, 0, 2, 1), (4, 16777214, 2, 0)]:
    n_units = 1 + max(0, span-2+5)//6
    own = units[used:used+n_units]
    for j,u in enumerate(own):
        p[37+used+j] = u | (128 if j else 0)
    a = own[0]*32
    uuid = key.to_bytes(16,'big')
    p[a:a+16] = uuid
    p[a+16:a+19] = base.to_bytes(3,'little')
    p[a+19:a+21] = cursor.to_bytes(2,'little')
    hist = []
    for j in range(span):
        if j == 3: continue  # missing revision
        pos = (cursor+j)%span
        at = a+22+pos*5 if pos<2 else own[1+(pos-2)//6]*32+(pos-2)%6*5
        hour, record = key+1, 70000+j
        p[at:at+2] = hour.to_bytes(2,'little')
        p[at+2:at+5] = record.to_bytes(3,'little')
        hist.append([base+j,str((hour<<32)|record)])
    queries.append({'key':uuid.hex(),'history':hist})
    used += n_units
p[28] = used
crc = 0xffffffff
for b in p:
    crc ^= b
    for _ in range(8):
        crc = (crc>>1) ^ (0x82f63b78 if crc&1 else 0)
struct.pack_into('<I',p,24,crc^0xffffffff)
Path(__file__).with_name('slotted.page').write_bytes(p)
Path(__file__).with_name('slotted.json').write_text(json.dumps(queries,indent=2)+'\n')
