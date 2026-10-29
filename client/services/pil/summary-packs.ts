// A PIL record containing ARRAY<BYTES>. Decode framing incrementally; each
// daily pack retains its own compression, digest, and persistent cache entry.
export const summaryBatchLimit = 1024;
export async function readSummaryPacks(response: Response, count: number, maximum: number,
  signal: AbortSignal, accept: (index: number, raw: Uint8Array) => Promise<void>): Promise<void> {
  if (!response.ok) throw Error(response.status === 409 ? 'Summary snapshot expired; repeat search' : `Summary retrieval failed (${response.status})`);
  if (response.headers.get('Content-Type')?.split(';')[0] !== 'application/pil') throw Error('Invalid summary array content type');
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
  const extended = async () => {
    const n = await byte() + (await byte())*256;
    if(n!==65535)return n;
    let value=0;for(let i=0;i<4;i++)value+=(await byte())*2**(8*i);
    if(value<65535||value===0xffffffff)throw Error('Invalid summary length');return value;
  };
  const cancel = () => { void reader.cancel(signal.reason).catch(() => {}); };
  signal.addEventListener('abort', cancel, {once:true});
  try {
    if(await byte()!==0xc0)throw Error('Invalid summary array mask');
    const total=await extended();
    if(await extended()!==count)throw Error('Summary array count mismatch');
    const ends:number[]=[];let previous=0;
    for(let i=0;i<count;i++){const end=await extended();if(end<previous)throw Error('Invalid summary offsets');ends.push(end);previous=end;}
    const extWidth=(n:number)=>n<65535?2:6;
    if(total!==extWidth(count)+ends.reduce((n,end)=>n+extWidth(end),0)+(ends.at(-1)??0))throw Error('Summary array boundary mismatch');
    for (let i = 0; i < count; i++) {
      const length = ends[i] - (i ? ends[i-1] : 0);
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
