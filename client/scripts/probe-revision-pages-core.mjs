// Allocation experiment only: deterministic synthetic bytes, not a production codec.
export const cases = [
  {name: 'positional-headers', count: 0},
  {name: 'positional-one', count: 1},
  {name: 'positional-two-distant', count: 2, distant: true},
  {name: 'positional-ten', count: 10},
  {name: 'positional-hundred', count: 100},
  {name: 'positional-full', count: 250},
  {name: 'positional-one-full-write', count: 1, fullWrite: true},
  {name: 'slotted-fixed-one', count: 1, slotted: true},
  {name: 'packed-variable-one', count: 1, packed: true, slotted: true},
  {name: 'packed-variable-ten', count: 10, packed: true, slotted: true},
  {name: 'whole-page-holes', count: 1, stride: 16},
  {name: 'whole-page-holes-reverse', count: 1, stride: 16, reverse: true},
].map((c, id) => ({...c, id: id + 1, objects: 4096}));
const segmentBytes = 2048;
function unitSize(c) { return c.packed ? 48 + c.count * 12 : segmentBytes; }
function offset(c, i) {
  if (c.packed) {
    const size = unitSize(c), perPage = Math.floor(4096 / size);
    return Math.floor(i / perPage) * 4096 + (i % perPage) * size;
  }
  return i * segmentBytes;
}
export function logicalSize(c) {
  return c.packed ? Math.ceil((offset(c, c.objects - 1) + unitSize(c)) / 4096) * 4096 : c.objects * segmentBytes;
}
function active(c, i) { return !c.stride || Math.floor(i / 2) % c.stride === 0; }
function unit(c, i, stage) {
  const data = new Uint8Array(unitSize(c));
  if (stage === 'zero-pages' || !active(c, i)) return data;
  const v = new DataView(data.buffer);
  // A unique marker lets the native runner identify the OPFS backing file.
  data.set([82, 86, 80, 48]); v.setUint32(4, c.id, true);
  for (let k = 8; k < 48; k++) data[k] = ((i + k) % 251) + 1;
  const n = stage === 'clear-records' ? 0 : stage === 'fill' ? Math.min(250, Math.max(c.count, 10)) : c.count;
  for (let j = 0; j < n; j++) {
    const slot = c.distant && j === 1 ? 249 : j;
    if (c.slotted) {
      // Two-byte pointer + two-byte revision ID + eight-byte event reference.
      const at = 48 + j * 12;
      v.setUint16(at, at + 2, true); v.setUint16(at + 2, j, true);
      v.setUint32(at + 4, i + 1, true); v.setUint32(at + 8, j + 1, true);
    } else {
      v.setUint32(48 + slot * 8, i + 1, true); v.setUint32(52 + slot * 8, slot + 1, true);
    }
  }
  return data;
}
export function operate(file, c, action) {
  const started = performance.now(); let writeCalls = 0, writtenBytes = 0;
  const write = (data, at) => {
    const n = file.write(data, at); if (n !== data.length) throw Error('Short write');
    writeCalls++; writtenBytes += n;
  };
  if (action === 'prepare') {
    file.truncate(logicalSize(c));
    for (let k = 0; k < c.objects; k++) {
      const i = c.reverse ? c.objects - 1 - k : k;
      if (!active(c, i)) continue;
      const data = unit(c, i, 'prepare'), at = offset(c, i);
      if (c.fullWrite || c.packed) write(data, at);
      else {
        write(data.subarray(0, 48), at);
        if (c.distant) {
          write(data.subarray(48, 56), at + 48);
          write(data.subarray(2040, 2048), at + 2040);
        } else if (c.count) write(data.subarray(48, 48 + c.count * (c.slotted ? 12 : 8)), at + 48);
      }
    }
    file.flush();
  } else if (action === 'fill' || action === 'clear-records') {
    if (c.packed) throw Error('Variable packing needs relocation to grow; not a gap-fill case');
    for (let i = 0; i < c.objects; i++) if (active(c, i)) {
      const data = unit(c, i, action);
      write(data.subarray(48), offset(c, i) + 48);
    }
    file.flush();
  } else if (action === 'zero-pages') {
    const zeros = new Uint8Array(4096);
    for (let at = 0; at < logicalSize(c); at += 4096) write(zeros, at);
    file.flush();
  } else if (action === 'truncate-half') {
    file.truncate(logicalSize(c) / 2); file.flush();
    if (file.size() !== logicalSize(c) / 2) throw Error('Truncation failed');
  }
  return {action, logical: file.size(), writtenBytes, writeCalls, milliseconds: performance.now() - started};
}
export function verify(file, c, stage = 'prepare') {
  if (file.size() !== logicalSize(c)) throw Error('Wrong size');
  const page = new Uint8Array(4096), expected = new Uint8Array(4096);
  const perPage = c.packed ? Math.floor(4096 / unitSize(c)) : 2;
  for (let at = 0, first = 0; at < logicalSize(c); at += 4096, first += perPage) {
    expected.fill(0);
    for (let j = 0; j < perPage && first + j < c.objects; j++) expected.set(unit(c, first + j, stage), j * unitSize(c));
    if (file.read(page, at) !== 4096) throw Error('Short read');
    for (let j = 0; j < page.length; j++) if (page[j] !== expected[j]) throw Error(`Mismatch ${c.name} ${stage} at ${at + j}`);
  }
  return {verified: true, stage, bytes: file.size()};
}
