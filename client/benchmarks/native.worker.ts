// Explicit benchmark entry, excluded from the app build. Uses an isolated OPFS namespace.
import wasmURL from '../assets/pil.wasm?url';
import { NativeEvent, NativeTrie } from '../services/pil/native-event.ts';
import { compile } from '../services/pil/metadata.ts';
import { Codec } from '../services/pil/wasm.ts';
import { PILStream } from '../services/pil/stream.ts';
import { collectNative } from '../services/pil/native-collect.ts';
import { createArchive } from '../services/store/runtime.ts';
import { NativeStore } from '../services/store/native.ts';
import { storagePort } from '../services/store/client.ts';
import { pacHeader } from '../services/pil/pac.ts';
import { INDEX_ROW_BYTES, pacPlacement, nativePAC } from '../services/pil/pac-native.ts';
let stages: Record<string, {
  ms: number;
  calls: number;
}> = {};
for (const name of ["record", "pac", "range", "finish"]) {
  const proto = NativeStore.prototype as any,
    fn = proto[name];
  proto[name] = async function (...args: any[]) {
    const t = performance.now();
    try {
      return await fn.apply(this, args);
    } finally {
      const key = name === "record" ? `record:${args[1].kind}${args[1].file ? ":" + args[1].file : ""}` : name;
      const s = stages[key] ??= {
        ms: 0,
        calls: 0
      };
      s.ms += performance.now() - t;
      s.calls++;
    }
  };
}
const clone = globalThis.structuredClone;
globalThis.structuredClone = (value, ...options) => {
  const t = performance.now();
  try {
    return clone(value, ...options);
  } finally {
    const key = (value as any)?.Definition ? 'clone:manifest' : (value as any)?.files ? 'clone:source' : 'clone:other',
      s = stages[key] ??= {
        ms: 0,
        calls: 0
      };
    s.ms += performance.now() - t;
    s.calls++;
  }
};
const digest = SubtleCrypto.prototype.digest;
SubtleCrypto.prototype.digest = async function (...args) {
  const t = performance.now();
  try {
    return await digest.apply(this, args);
  } finally {
    const s = stages.checksum ??= {
      ms: 0,
      calls: 0
    };
    s.ms += performance.now() - t;
    s.calls++;
  }
};
let wire: Uint8Array, headers: Headers, tap: string, from: string, to: string;
const modulePromise = fetch(wasmURL).then(r => WebAssembly.compileStreaming(r));
let stats = {
  opens: 0,
  writes: 0,
  writeBytes: 0,
  reads: 0,
  readBytes: 0,
  flushes: 0,
  metadataCommits: 0,
  metadataBytes: 0
};
const reset = () => {
  stats = {
    opens: 0,
    writes: 0,
    writeBytes: 0,
    reads: 0,
    readBytes: 0,
    flushes: 0,
    metadataCommits: 0,
    metadataBytes: 0
  };
};
const prototype = FileSystemFileHandle.prototype as FileSystemFileHandle & {
  createSyncAccessHandle: () => Promise<any>;
};
const original = prototype.createSyncAccessHandle;
prototype.createSyncAccessHandle = async function () {
  const handle = await original.call(this);
  stats.opens++;
  return new Proxy(handle, {
    get(target, key) {
      if (key === 'write') return (b: Uint8Array, options: unknown) => {
        const n = target.write(b, options);
        stats.writes++;
        stats.writeBytes += n;
        return n;
      };
      if (key === 'read') return (b: Uint8Array, options: unknown) => {
        const n = target.read(b, options);
        stats.reads++;
        stats.readBytes += n;
        return n;
      };
      if (key === 'flush') return () => {
        stats.flushes++;
        return target.flush();
      };
      const v = target[key];
      return typeof v === 'function' ? v.bind(target) : v;
    }
  });
};
const create = FileSystemFileHandle.prototype.createWritable;
FileSystemFileHandle.prototype.createWritable = async function (options) {
  const writer = await create.call(this, options),
    write = writer.write.bind(writer),
    close = writer.close.bind(writer);
  const name = this.name;
  writer.write = async data => {
    if (ArrayBuffer.isView(data)) stats.metadataBytes += data.byteLength;
    return write(data);
  };
  writer.close = async () => {
    await close();
    if (name === 'commit.pil') stats.metadataCommits++;
  };
  return writer;
};
self.onmessage = async ({
  data
}) => {
  try {
    if (data.kind === 'prepare') {
      ({
        tap,
        from,
        to
      } = data);
      const start = performance.now();
      if (data.fixture) {
        const meta = await (await fetch(data.fixture + '.json')).json();
        ({
          tap,
          from,
          to
        } = meta);
        headers = new Headers(meta.headers);
        wire = new Uint8Array(await (await fetch(data.fixture)).arrayBuffer());
        self.postMessage({
          id: data.id,
          result: {
            wireBytes: wire.length,
            downloadMs: performance.now() - start
          }
        });
        return;
      }
      const query = new URLSearchParams({
        format: 'native',
        tap,
        from,
        to
      });
      const response = await fetch('/collect?' + query, {
        headers: {
          Accept: 'application/pil',
          'PIL-Version': '2',
          'PIL-Features': 'native-ranges,native-state-pages,adaptive-integers'
        },
        cache: 'no-store'
      });
      if (!response.ok) throw Error(await response.text());
      headers = new Headers(response.headers);
      wire = new Uint8Array(await response.arrayBuffer());
      self.postMessage({
        id: data.id,
        result: {
          wireBytes: wire.length,
          downloadMs: performance.now() - start
        }
      });
      return;
    }
    const codec = new Codec(await WebAssembly.instantiate(await modulePromise));
    if (data.kind === 'inflate-variants') {
      const sizes = await (await fetch('/after/variants.json')).json(),
        result = [];
      for (const [name, size] of Object.entries(sizes)) {
        const raw = new Uint8Array(await (await fetch('/after/' + name + '.pac')).arrayBuffer()),
          length = new DataView(raw.buffer).getUint32(8, true);
        for (let i = 0; i < 20; i++) codec.inflate(raw.subarray(16), length);
        const samples = [];
        for (let pass = 0; pass < 7; pass++) {
          const start = performance.now();
          for (let i = 0; i < 256; i++) codec.inflate(raw.subarray(16), length);
          samples.push(performance.now() - start);
        }
        result.push({
          name,
          ...(size as object),
          samples_256_hours_ms: samples
        });
      }
      self.postMessage({
        id: data.id,
        result
      });
      return;
    }
    const codecStats = {
      uploadMs: 0,
      uploadedBytes: 0,
      uploads: 0,
      scanMs: 0,
      scanStructuralMs: 0,
      pacMs: 0,
      pacStructuralMs: 0,
      pacs: 0,
      inflateMs: 0,
      inflates: 0,
      verifyCRC32CMs: 0,
      checksums: 0
    };
    for (const key of ['upload', 'scan', 'scanStructural', 'pac', 'pacStructural', 'inflate', 'verifyCRC32C'] as const) {
      const fn = (codec[key] as Function).bind(codec);
      (codec as any)[key] = (...args: any[]) => {
        const t = performance.now();
        try {
          if (key === 'upload') {
            codecStats.uploads++;
            codecStats.uploadedBytes += args[0].byteLength;
          }
          if (key === 'pac' || key === 'pacStructural') codecStats.pacs++;
          if (key === 'verifyCRC32C') codecStats.checksums++;
          if (key === 'inflate') codecStats.inflates++;
          return fn(...args);
        } finally {
          codecStats[`${key}Ms`] += performance.now() - t;
        }
      };
    }
    {
      if (data.kind === 'decode') {
        let hours = 0,
          events = 0,
          payloadBytes = 0;
        const start = performance.now();
        const parser = new PILStream(codec, new Map(), () => {}, () => {}, async () => {
          throw Error('Expanded event');
        }, async r => {
          if (r.kind === 'pac') {
            hours++;
            events += r.bounds.length / 2;
            payloadBytes += r.bytes.length;
          }
        });
        try {
          for (let at = 0; at < wire.length; at += 65536) await parser.feed(wire.subarray(at, at + 65536));
          parser.finish();
        } finally {
          parser.close();
        }
        self.postMessage({
          id: data.id,
          result: {
            decodeMs: performance.now() - start,
            hours,
            events,
            payloadBytes,
            codec: codecStats
          }
        });
        return;
      }
      const token = 'benchmark-hour-blocks',
        execute = createArchive(),
        commands: Record<string, number> = {};
      let archiveMs = 0;
      const namespace = data.ipc ? [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(token)))].map(n => n.toString(16).padStart(2, '0')).join('') : token;
      const storageWorker = data.ipc ? new Worker(new URL('../services/store/worker.ts', import.meta.url), {
        type: 'module'
      }) : undefined;
      const channel = storageWorker ? new MessageChannel() : undefined;
      const remote = channel ? storagePort(channel.port1) : undefined;
      if (channel) storageWorker!.postMessage({
        kind: 'connect'
      }, [channel.port2]);
      const root = await navigator.storage.getDirectory();
      const erase = async () => {
        try {
          await (await root.getDirectoryHandle('snoot-store')).removeEntry(namespace, {
            recursive: true
          });
        } catch (e) {
          if (!(e instanceof DOMException && e.name === 'NotFoundError')) throw e;
        }
      };
      if (!data.warm) await erase();
      reset();
      stages = {};
      const started = performance.now();
      const archive = async (_: string, command: any) => {
        commands[command.kind] = (commands[command.kind] ?? 0) + 1;
        const start = performance.now();
        try {
          return remote ? await remote(token, command) : await navigator.locks.request('trie-trunk-files-v1', () => execute(namespace, command));
        } finally {
          archiveMs += performance.now() - start;
        }
      };
      try {
        const sources = await collectNative({
          tap,
          from,
          to,
          token: '',
          signal: new AbortController().signal,
          codec,
          definitions: new Map(),
          archive,
          fetcher: async () => new Response(wire.slice().buffer, {
            headers
          })
        });
        const elapsedMs = performance.now() - started,
          writeArchiveMs = archiveMs,
          io = {
            ...stats
          },
          timings = structuredClone(stages),
          writeCommands = {
            ...commands
          };
        const coverage = (await archive('', {
          kind: 'native-coverage',
          tap
        })).nativeCoverage;
        let sampleRows = 0;
        for (const source of sources) {
          const page = (await archive('', {
            kind: 'native-page',
            source,
            from: Date.parse(from),
            to: Date.parse(to),
            limit: 10
          })).nativePage!;
          sampleRows += page.rows.length / INDEX_ROW_BYTES;
        }
        const retrieval = [];
        let uuidHash = '';
        for (let pass = 0; pass < 5; pass++) {
          const began = performance.now();
          let readMs = 0,
            materializeMs = 0,
            total = 0;
          const ids = [];
          for (const source of sources) {
            const deps = (await archive('', {
              kind: 'native-dependencies',
              source
            })).nativeDependencies!;
            const mapping = compile(deps.definition, tap),
              trie = new NativeTrie(mapping);
            for (const part of deps.trie) trie.append(part.start, part.bytes);
            const t = performance.now(),
              page = (await archive('', {
                kind: 'native-page',
                source,
                from: Date.parse(from),
                to: Date.parse(to),
                limit: 100
              })).nativePage!;
            readMs += performance.now() - t;
            const m = performance.now(),
              payload = page.payload;
            for (let j = 0; j < page.bases.length; j++) {
              const event = new NativeEvent(page.rows.subarray(j * INDEX_ROW_BYTES, (j + 1) * INDEX_ROW_BYTES), payload.subarray(page.offsets[j * 2], page.offsets[j * 2 + 1]), trie, page.common?.[j], page.bases[j]);
              ids.push(...Array.from(event.uuid, c => c.charCodeAt(0)));
              for (const key of event.orderedKeys()) if (!mapping.definition.fields[key].name.startsWith('@')) event.read(key);
              total++;
            }
            materializeMs += performance.now() - m;
          }
          retrieval.push({
            elapsedMs: performance.now() - began,
            readMs,
            materializeMs,
            events: total
          });
          uuidHash = [...new Uint8Array(await crypto.subtle.digest('SHA-256', Uint8Array.from(ids)))].map(n => n.toString(16).padStart(2, '0')).join('');
        }
        let verifiedHours = 0,
          verifiedEvents = 0;
        if (data.verifyAll) {
          const verifyCodec = new Codec(await WebAssembly.instantiate(await modulePromise));
          const indexes = new Map<number, Uint8Array>();
          const parser = new PILStream(verifyCodec, new Map(), () => {}, () => {}, async () => {}, async r => {
            if (r.kind === 'chunk' && r.file === 'index') indexes.set(r.start, r.bytes);
            if (r.kind !== 'pac') return;
            const head = pacHeader(r.bytes, 0, 0xffffffff, new Set([1, nativePAC]))!,
              placement = pacPlacement(head),
              index = indexes.get(Number(placement.firstRow));
            const page = (await archive('', {
              kind: 'native-page',
              source: sources[0],
              from: Number(placement.from),
              to: Number(placement.to),
              limit: 4096
            })).nativePage!;
            const expected = r.bytes.subarray(head.start, head.end),
              count = r.bounds.length / 2;
            if (count > 4096 || !index || page.rows.length !== count * INDEX_ROW_BYTES || page.payload.length !== expected.length || page.payload.some((v, i) => v !== expected[i])) throw Error('Hourly payload readback mismatch');
            for (let i = 0; i < count; i++) {
              for (let j = 0; j < INDEX_ROW_BYTES; j++) if (page.rows[i * INDEX_ROW_BYTES + j] !== index[i * INDEX_ROW_BYTES + j]) throw Error('Event identity readback mismatch');
              if (page.offsets[i * 2] !== r.bounds[i * 2] - head.start || page.offsets[i * 2 + 1] !== r.bounds[i * 2 + 1] - head.start) throw Error('Event boundary readback mismatch');
            }
            verifiedHours++;
            verifiedEvents += count;
            indexes.delete(Number(placement.firstRow));
          });
          try {
            for (let at = 0; at < wire.length; at += 65536) await parser.feed(wire.subarray(at, at + 65536));
            parser.finish();
          } finally {
            parser.close();
          }
        }
        self.postMessage({
          id: data.id,
          result: {
            mode: data.ipc ? 'production-worker-port' : 'instrumented-same-worker',
            namespace,
            elapsedMs,
            archiveMs: writeArchiveMs,
            commands: writeCommands,
            io: data.ipc ? undefined : io,
            stages: timings,
            codec: codecStats,
            sources: sources.length,
            coverageRows: coverage?.reduce((n, s) => n + s.Index.reduce((n, r) => n + r.End - r.Start, 0), 0),
            sampleRows,
            retrieval,
            uuidHash,
            verifiedHours,
            verifiedEvents
          }
        });
      } finally {
        remote?.close?.();
        storageWorker?.terminate();
      }
    }
  } catch (error) {
    self.postMessage({
      id: data.id,
      error: String(error)
    });
  }
};
