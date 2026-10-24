// Isolated storage semantics probe; no application data or codec is involved.
const PAGE = 16384, SIZE = 64 * 1024 * 1024;
const positions = [SIZE - PAGE, 0, 16 * 1024 * 1024, 32 * 1024 * 1024];
const gap = 48 * 1024 * 1024;
const assert = (ok, message) => { if (!ok) throw Error(message); };
const pattern = at => Uint8Array.from({length: PAGE}, (_, i) => (at / PAGE + i * 31 + 7) % 251 + 1);
const usage = async () => { const e = await navigator.storage.estimate(); return {usage: e.usage, quota: e.quota}; };
function write(f, bytes, at) {
  let n = 0;
  while (n < bytes.length) { const k = f.write(bytes.subarray(n), {at: at + n}); assert(k > 0, 'short write'); n += k; }
}
function read(f, at) { const b = new Uint8Array(PAGE); assert(f.read(b, {at}) === PAGE, 'short read'); return b; }
onmessage = async ({data: {mode, action, isolateModes = false}}) => {
  const handles = [];
  try {
    const root = await navigator.storage.getDirectory();
    assert(['seek', 'truncate', 'zero-control', 'keyed-pages'].includes(mode), 'unknown mode');
    const directory = isolateModes ? `sparse-validation-${mode}` : 'sparse-validation';
    if (action === 'prepare' || action === 'delete') {
      try { await root.removeEntry(directory, {recursive: true}); } catch (e) { if (e.name !== 'NotFoundError') throw e; }
    }
    if (action === 'delete') { postMessage({result: true}); return; }
    const dir = await root.getDirectoryHandle(directory, {create: true});
    async function handle(at) {
      const name = mode === 'keyed-pages' ? `${at / PAGE}.sods` : 'data.sods';
      const h = await dir.getFileHandle(name, {create: action === 'prepare' || action === 'fill'});
      const f = await h.createSyncAccessHandle(); handles.push(f); return f;
    }
    const before = await usage(), start = performance.now();
    let f;
    if (mode !== 'keyed-pages') f = await handle(0);
    if (action === 'prepare') {
      if (mode === 'truncate') f.truncate(SIZE);
      if (mode === 'zero-control') {
        const zeros = new Uint8Array(1024 * 1024);
        for (let at = 0; at < SIZE; at += zeros.length) write(f, zeros, at);
      }
      for (const at of positions) {
        const dest = f ?? await handle(at);
        write(dest, pattern(at), f ? at : 0); dest.flush();
      }
    } else {
      if (action === 'fill') {
        if (f) assert(read(f, gap).every(v => v === 0), 'gap not zero before fill');
        const dest = f ?? await handle(gap);
        write(dest, pattern(gap), f ? gap : 0); dest.flush();
        if (!f) { dest.close(); handles.splice(handles.indexOf(dest), 1); }
      }
      for (const at of [...positions, ...(action === 'fill' || action === 'verify-filled' ? [gap] : [])]) {
        const src = f ?? await handle(at), expected = pattern(at), got = read(src, f ? at : 0);
        assert(got.every((v, i) => v === expected[i]), `changed bytes at ${at}`);
      }
      if (f) {
        assert(f.getSize() === SIZE, 'logical size changed');
        assert(read(f, 8 * 1024 * 1024).every(v => v === 0), 'unfilled gap changed');
      }
    }
    const elapsedMs = performance.now() - start;
    for (const h of handles.splice(0)) h.close();
    const files = [];
    for await (const [name, h] of dir.entries()) files.push({name, size: (await h.getFile()).size});
    postMessage({result: {action, mode, before, after: await usage(), elapsedMs, files, verified: action !== 'prepare'}});
  } catch (e) {
    for (const h of handles) try { h.close(); } catch {}
    postMessage({error: {name: e.name, message: e.message}});
  }
};
