import { NativeEvent, NativeTrie } from '../seeds/index.ts';
import type { LogEvent } from '../dictionary.ts';
import { PILEvent } from './event.ts';
import { compile } from './metadata.ts';
import type { ViewResponse } from './view.ts';

/** Recreate byte-backed views; never expand event objects for tab persistence. */
export function viewEvents(view: ViewResponse): LogEvent[] {
  const { definitions, bindings, batch } = view;
  const streams = new Map(bindings.map(b => {
    const definition = definitions.find(d => d.digest === b.digest);
    if (!definition) throw Error('Missing page definition');
    return [b.stream, compile(definition, b.tap, [])] as const;
  }));
  if (batch.rows.length % 3 || batch.rows.length / 3 !== view.result.page.length)
    throw Error('Invalid saved page rows');
  const events: LogEvent[] = [];
  const tries=new Map<number,NativeTrie>();
  const loaded=new Map<number,Set<object>>();
  let end = 0;
  for (let i = 0; i < batch.rows.length; i += 3) {
    const [stream, start, through] = batch.rows.subarray(i, i + 3);
    const mapping = streams.get(stream);
    if (!mapping || start !== end || through <= start || through > batch.bytes.byteLength)
      throw Error('Invalid page bounds');
    const native=view.native?.[i/3],bytes=new Uint8Array(batch.bytes,start,through-start);
    if(native){let trie=tries.get(stream);if(!trie){trie=new NativeTrie(mapping);tries.set(stream,trie)}
      let seen=loaded.get(stream);if(!seen){seen=new Set();loaded.set(stream,seen)}
      if(!seen.has(native.trie)){for(const chunk of native.trie)trie.append(chunk.start,chunk.bytes);seen.add(native.trie)}
      events.push(new NativeEvent(native.row,bytes,trie,batch.common?.[i/3],native.base));
    }else events.push(new PILEvent(bytes,mapping,batch.common?.[i/3]));
    end = through;
  }
  if (end !== batch.bytes.byteLength) throw Error('Invalid page length');
  return events;
}
