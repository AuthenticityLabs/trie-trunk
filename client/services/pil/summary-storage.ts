// Compressed daily packets share a byte budget, independent of event storage.
export const summaryStorageLimit = 256 * 1024 * 1024;
let writes: Promise<unknown> = Promise.resolve();
let sizes: Map<string, number> | undefined;
export function storeSummaryPacket(cache: Cache, url: string, raw: Uint8Array): Promise<void> {
  const write = writes.catch(() => {}).then(async () => {
    if (raw.byteLength > summaryStorageLimit) throw Error('Summary packet exceeds local storage budget');
    if (!sizes) {
      const entries = new Map<string, number>();
      for (const key of await cache.keys()) {
        const response = await cache.match(key);
        if (response) entries.set(key.url, Number(response.headers.get('Content-Length')) || (await response.arrayBuffer()).byteLength);
      }
      sizes = entries;
    }
    let total = [...sizes.values()].reduce((sum, n) => sum + n, 0) - (sizes.get(url) ?? 0);
    sizes.delete(url);
    for (const [key, size] of sizes) {
      if (total + raw.byteLength <= summaryStorageLimit) break;
      await cache.delete(key); sizes.delete(key); total -= size;
    }
    await cache.put(url, new Response(raw.slice().buffer, {headers:{'Content-Length':String(raw.byteLength)}}));
    sizes.set(url, raw.byteLength);
  });
  writes = write;
  return write;
}

// Bound queued compressed bytes so slow disks cannot accumulate an unbounded
// copy of foreground responses. Explicit subscription writes still await disk.
export const summaryWriteQueueLimit = 8 * 1024 * 1024;
let queuedBytes = 0;
export function queueSummaryPacket(cache: Cache, url: string, raw: Uint8Array): boolean {
  if (queuedBytes + raw.byteLength > summaryWriteQueueLimit) return false;
  queuedBytes += raw.byteLength;
  const release = () => { queuedBytes -= raw.byteLength; };
  void storeSummaryPacket(cache, url, raw).then(release, release);
  return true;
}
