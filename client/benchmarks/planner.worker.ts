import wasmURL from '../assets/pil.wasm?url';
import { Codec } from '../services/pil/wasm.ts';
import { collectNative } from '../services/pil/native-collect.ts';
import { ViewCache } from '../services/pil/view.ts';
import { storagePort } from '../services/store/client.ts';
self.onmessage = async ({
  data
}) => {
  const token = 'planner-benchmark-' + data.variant,
    root = await navigator.storage.getDirectory();
  const hash = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(token));
  const namespace = [...new Uint8Array(hash)].map(n => n.toString(16).padStart(2, '0')).join('');
  const erase = async () => {
    try {
      await (await root.getDirectoryHandle('snoot-store')).removeEntry(namespace, {
        recursive: true
      });
    } catch (e) {
      if ((e as DOMException).name !== 'NotFoundError') throw e;
    }
  };
  const storage = new Worker(new URL('../services/store/worker.ts', import.meta.url), {
      type: 'module'
    }),
    channel = new MessageChannel(),
    remote = storagePort(channel.port1);
  storage.postMessage({
    kind: 'connect'
  }, [channel.port2]);
  let commands: Record<string, number> = {};
  const archive: typeof remote = async (_, command) => {
    commands[command.kind] = (commands[command.kind] ?? 0) + 1;
    return remote(token, command);
  };
  try {
    await erase();
    const codec = new Codec(await WebAssembly.instantiate(await WebAssembly.compileStreaming(fetch(wasmURL)))),
      definitions = new Map();
    const meta = await (await fetch('/fixture/dense.pil.json')).json(),
      wire = await (await fetch('/fixture/dense.pil')).arrayBuffer();
    const started = performance.now();
    const sources = await collectNative({
      tap: meta.tap,
      from: meta.from,
      to: meta.to,
      token,
      signal: new AbortController().signal,
      codec,
      definitions,
      archive,
      fetcher: async () => new Response(wire.slice(0), {
        headers: meta.headers
      })
    });
    const collectMs = performance.now() - started;
    self.postMessage({
      stage: 'collected',
      collectMs,
      commands
    });
    const deps = await Promise.all(sources.map(async source => (await archive(token, {
      kind: 'native-dependencies',
      source
    })).nativeDependencies!));
    const trunk: any = {
      version: 'benchmark',
      catalog: {
        files: deps.flatMap(({
          source: s
        }) => ['tri', 'pods'].map(kind => ({
          kind,
          path: s.tap.slice(0, s.tap.lastIndexOf('/')) + '/0/' + s.tap.split('/').at(-1) + '.' + kind,
          definition: s.digest,
          bootstrap: false
        })))
      },
      bindings: deps.map(({
        source: s
      }, i) => ({
        tap: s.tap,
        digest: s.digest,
        stream: i + 1
      })),
      definitions: [...definitions.values()]
    };
    const cache = new ViewCache(async () => {
      throw Error('Warm search must not fetch');
    });
    cache.setTrunk(trunk);
    const query = {
      tap: meta.tap,
      from: meta.from,
      to: meta.to,
      size: 100,
      query: {
        filters: [],
        order: 'asc' as const
      }
    };
    const run = async (q: typeof query & {
      after?: string;
    }) => {
      commands = {};
      let first: number | undefined;
      const t = performance.now();
      const result = await cache.query(q, token, new AbortController().signal, codec, definitions, archive, false, undefined, () => {
        first ??= performance.now() - t;
        self.postMessage({
          stage: 'first-page',
          ms: first
        });
      });
      return {
        result,
        metrics: {
          ms: performance.now() - t,
          firstPageMs: first,
          commands,
          matched: result.result.summary?.count,
          loaded: result.result.page.length
        }
      };
    };
    const first = await run(query);
    self.postMessage({
      stage: 'scan',
      metrics: first.metrics
    });
    const next = await run({
      ...query,
      after: first.result.result.next
    });
    if (first.result.result.summary?.count !== meta.events || next.result.result.summary?.count !== meta.events || next.result.result.page.length !== 100 || first.result.result.page.some(p => next.result.result.page.some(n => n.key === p.key))) throw Error('Planner results failed parity');
    self.postMessage({
      result: {
        variant: data.variant,
        events: meta.events,
        hours: meta.hours,
        collectMs,
        scan: first.metrics,
        nextPage: next.metrics,
        scope: 'Chromium + WASM + production storage worker + durable OPFS. Cached fixture transport; excludes network and DOM rendering.'
      }
    });
  } catch (error) {
    self.postMessage({
      error: String(error),
      stack: (error as Error).stack
    });
  } finally {
    remote.close?.();
    storage.terminate();
    await erase();
    self.postMessage({
      cleaned: true
    });
  }
};
