// Isolated addressing prototype. Payloads are deterministic bytes, not PIL frames.
// Never opens the application's OPFS directories.
const WIDTH = 26;
const epoch = 1792454400000;
let root, dir, idx, sodsFile, chunks, active;
const check = (ok, message) => { if (!ok) throw Error(message); };
const writeAll = (file, bytes, at) => {
  let n = 0;
  while (n < bytes.length) {
    const wrote = file.write(bytes.subarray(n), {at: at + n});
    check(wrote > 0, "short write");
    n += wrote;
  }
};
function read(file, at, length) {
  const bytes = new Uint8Array(length);
  check(file.read(bytes, {at}) === length, "short read");
  return bytes;
}
function stamp(bytes, at, time) {
  for (let i = 5; i >= 0; i--) { bytes[at + i] = time % 256; time = Math.floor(time / 256); }
}
function timeAt(row) {
  const bytes = read(idx, row * WIDTH, 6);
  return bytes.reduce((v, b) => v * 256 + b, 0);
}
const lengthAt = row => 31 + row % 67;
const byteAt = (row, i) => (row * 17 + i * 29) % 256;
async function persist() {
  const handle = await dir.getFileHandle("coverage.json", {create: true});
  const stream = await handle.createWritable();
  await stream.write(JSON.stringify(chunks));
  await stream.close();
}
async function append(start, count) {
  if (chunks.some(c => c.start === start && c.count === count)) return false;
  check(!chunks.some(c => start < c.start + c.count && start + count > c.start), "overlap");
  const base = sodsFile.getSize(), rows = new Uint8Array(count * WIDTH);
  const view = new DataView(rows.buffer);
  let end = base;
  const payloads = [];
  for (let i = 0; i < count; i++) {
    const row = start + i, bytes = new Uint8Array(lengthAt(row));
    for (let j = 0; j < bytes.length; j++) bytes[j] = byteAt(row, j);
    payloads.push(bytes);
    stamp(rows, i * WIDTH, epoch + row);
    rows[i * WIDTH + 6] = 0x70; // UUIDv7 version
    rows[i * WIDTH + 8] = 0x80; // variant
    view.setUint16(i * WIDTH + 16, row % 65536, true);
    end += bytes.length;
    view.setBigUint64(i * WIDTH + 18, BigInt(end), true);
  }
  const payload = new Uint8Array(end - base);
  let offset = 0;
  for (const bytes of payloads) { payload.set(bytes, offset); offset += bytes.length; }
  writeAll(sodsFile, payload, base);
  writeAll(idx, rows, start * WIDTH);
  sodsFile.flush(); idx.flush();
  chunks.push({start, count, base});
  chunks.sort((a, b) => a.start - b.start);
  await persist();
  return true;
}
function resolve(row) {
  const c = chunks.find(c => row >= c.start && row < c.start + c.count);
  if (!c) return null; // Never interpret holes as UUIDs.
  const end = Number(new DataView(read(idx, row * WIDTH + 18, 8).buffer).getBigUint64(0, true));
  const start = row === c.start ? c.base :
    Number(new DataView(read(idx, (row - 1) * WIDTH + 18, 8).buffer).getBigUint64(0, true));
  return read(sodsFile, start, end - start);
}
function lower(c, target) {
  let lo = c.start, hi = c.start + c.count;
  while (lo < hi) {
    const mid = Math.floor((lo + hi) / 2);
    if (timeAt(mid) < target) lo = mid + 1; else hi = mid;
  }
  return lo;
}
function search(from, to) {
  const results = [];
  for (const c of chunks) {
    // A real directory caches actual first/last timestamps.
    if (epoch + c.start >= to || epoch + c.start + c.count <= from) continue;
    const a = lower(c, from), b = lower(c, to);
    for (let i = a; i < b; i++) results.push(i);
  }
  return results;
}
function verify(total) {
  let records = 0;
  for (const c of chunks) for (let row = c.start; row < c.start + c.count; row++) {
    const bytes = resolve(row);
    check(timeAt(row) === epoch + row, "source row identity changed");
    check(bytes.length === lengthAt(row), "bad payload boundary");
    check(bytes.every((b, i) => b === byteAt(row, i)), "wrong payload");
    records++;
  }
  check(resolve(0) === null, "hole was treated as an event");
  check(read(idx, 0, WIDTH).every(b => b === 0), "gap is not zero-filled");
  check(lower({start: 0, count: total}, epoch + 1000) !== 1000,
    "negative control did not expose ordinary binary search across holes");
  const filled = chunks.find(c => c.start === 2000);
  const precedingEnd = Number(new DataView(read(idx, 1999 * WIDTH + 18, 8).buffer).getBigUint64(0, true));
  check(precedingEnd !== filled.base, "negative control did not expose the chunk payload boundary");
  let seed = 7;
  const next = () => (seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0);
  const begin = performance.now();
  for (let i = 0; i < 1000; i++) {
    const from = next() % total, to = Math.min(total, from + next() % 10000);
    const expected = [];
    for (const c of chunks) for (let row = Math.max(from, c.start); row < Math.min(to, c.start + c.count); row++) expected.push(row);
    check(JSON.stringify(search(epoch + from, epoch + to)) === JSON.stringify(expected), "range search mismatch");
  }
  const validationMs = performance.now() - begin;
  // Measure actual indexed probes, separately from the brute-force correctness oracle.
  const start = performance.now();
  for (let i = 0; i < 1000; i++) {
    const c = chunks[i % chunks.length];
    search(epoch + c.start + 100, epoch + c.start + 110);
  }
  return {recordsVerified: records, rangeChecks: 1000, validationMs,
    ordinaryBinarySearchAcrossHolesIncorrect: true, chunkPayloadBaseRequired: true,
    populatedRangeLookupUs: (performance.now() - start) * 1000 / 1000};
}
async function open() {
  idx = await (await dir.getFileHandle("index", {create: true})).createSyncAccessHandle();
  sodsFile = await (await dir.getFileHandle("sods", {create: true})).createSyncAccessHandle();
}
onmessage = async ({data}) => {
  try {
    if (data.kind === "prepare") {
      root = await navigator.storage.getDirectory();
      active = data.name;
      dir = await root.getDirectoryHandle(active, {create: true});
      chunks = [];
      await open();
      const before = await navigator.storage.estimate(), begin = performance.now();
      if (data.mode === "padded") {
        const zeros = new Uint8Array(1024 * 1024);
        for (let at = 0; at < data.total * WIDTH; at += zeros.length)
          writeAll(idx, zeros.subarray(0, Math.min(zeros.length, data.total * WIDTH - at)), at);
        idx.flush();
      }
      const paddingMs = performance.now() - begin;
      const ingest = performance.now();
      // Last range first: force the full logical extent, then fill earlier holes.
      await append(data.total - 1000, 1000);
      await append(1000, 1000);
      await append(Math.floor(data.total / 2), 1000);
      const ingestMs = performance.now() - ingest;
      const retainedBefore = read(idx, (data.total - 1000) * WIDTH, 1000 * WIDTH);
      const fill = performance.now();
      await append(2000, 1000);
      const fillMs = performance.now() - fill;
      const retainedAfter = read(idx, (data.total - 1000) * WIDTH, 1000 * WIDTH);
      check(retainedBefore.every((b, i) => b === retainedAfter[i]), "gap fill moved existing references");
      const sizes = [idx.getSize(), sodsFile.getSize()];
      check(await append(2000, 1000) === false, "duplicate range appended");
      check(sizes[0] === idx.getSize() && sizes[1] === sodsFile.getSize(), "duplicate grew files");
      const result = verify(data.total);
      const after = await navigator.storage.estimate();
      idx.close(); sodsFile.close();
      const closed = await navigator.storage.estimate();
      postMessage({result: {...result, mode: data.mode, totalRows: data.total, paddingMs, ingestMs, fillMs,
        indexBytes: sizes[0], payloadBytes: sizes[1], receivedIndexBytes: 4000 * WIDTH,
        quotaUsageBefore: before.usage, quotaUsageAfter: after.usage,
        quotaDelta: after.usage - before.usage, usageDetails: after.usageDetails,
        closedQuotaUsage: closed.usage, closedQuotaDelta: closed.usage - before.usage}});
    } else if (data.kind === "reopen") {
      root = await navigator.storage.getDirectory();
      active = data.name;
      dir = await root.getDirectoryHandle(active);
      chunks = JSON.parse(await (await (await dir.getFileHandle("coverage.json")).getFile()).text());
      const before = await navigator.storage.estimate();
      await open();
      const result = verify(data.total);
      const opened = await navigator.storage.estimate();
      idx.close(); sodsFile.close();
      const estimate = await navigator.storage.estimate();
      postMessage({result: {...result, quotaUsage: estimate.usage,
        quotaBeforeOpen: before.usage, quotaWhileOpen: opened.usage}});
    } else if (data.kind === "delete") {
      await root.removeEntry(active, {recursive: true});
      postMessage({result: true});
    }
  } catch (error) { postMessage({error: error.stack || String(error)}); }
};
