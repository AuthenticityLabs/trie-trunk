// A definite CBOR array of byte strings. Decode framing incrementally; each
// daily pack retains its own compression, digest, and persistent cache entry.
export const summaryBatchLimit = 1024;
export async function readSummaryPacks(response: Response, count: number, maximum: number,
  signal: AbortSignal, accept: (index: number, raw: Uint8Array) => Promise<void>): Promise<void> {
  if (!response.ok) throw Error(response.status === 409 ? 'Summary snapshot expired; repeat search' : `Summary retrieval failed (${response.status})`);
  if (response.headers.get('Content-Type')?.split(';')[0] !== 'application/cbor') throw Error('Invalid summary array content type');
  const reader = response.body?.getReader();
  if (!reader) throw Error('Missing summary array');
  let chunk: Uint8Array = new Uint8Array(0), at = 0;
  const available = async () => {
    signal.throwIfAborted();
    while (at === chunk.length) {
      const part = await reader.read();
      signal.throwIfAborted();
      if (part.done) return false;
      chunk = part.value; at = 0;
    }
    return true;
  };
  const byte = async () => {
    if (!await available()) throw Error('Truncated summary array');
    return chunk[at++];
  };
  const head = async (major: number) => {
    const tag = await byte(), width = tag & 31;
    if (tag >> 5 !== major || width > 27) throw Error('Invalid summary array framing');
    if (width < 24) return width;
    let n = 0;
    for (let i = 0; i < 2 ** (width - 24); i++) n = n * 256 + await byte();
    if (!Number.isSafeInteger(n)) throw Error('Invalid summary array length');
    return n;
  };
  const cancel = () => { void reader.cancel(signal.reason).catch(() => {}); };
  signal.addEventListener('abort', cancel, {once:true});
  try {
    if (await head(4) !== count) throw Error('Summary array count mismatch');
    for (let i = 0; i < count; i++) {
      const length = await head(2);
      if (length < 32 || length > maximum) throw Error('Summary exceeds limit');
      const raw = new Uint8Array(length);
      let written = 0;
      while (written < length) {
        if (!await available()) throw Error('Truncated summary pack');
        const n = Math.min(length - written, chunk.length - at);
        raw.set(chunk.subarray(at, at + n), written); written += n; at += n;
      }
      signal.throwIfAborted();
      await accept(i, raw);
    }
    if (await available()) throw Error('Trailing summary array data');
    signal.throwIfAborted();
  } finally {
    signal.removeEventListener('abort', cancel);
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}
