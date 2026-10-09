import { cbor } from "./cbor.ts";
import type { PILDefinition } from "./layout.ts";
import type { Archive } from "../store/protocol.ts";
import { Codec } from "./wasm.ts";
import {
  PILStream,
  consume,
  checkResponse,
  pilType,
  type StoredDefinition,
} from "./stream.ts";
import type { Binding, Metadata } from "./metadata.ts";
import type { Request, Reply } from "./protocol.ts";
import { collectionURL } from "./selection.ts";
export function workerService(
  modulePromise: Promise<WebAssembly.Module>,
  fetcher: typeof fetch = fetch,
  archive?: Archive,
) {
  type Session = {
    codec: Codec;
    definitions: Map<string, StoredDefinition>;
    bindings: Binding[];
    negotiation: string;
    loading?: Promise<void>;
  };
  const sessions = new Map<string, Promise<Session>>();
  const credentials = new Map<
    string,
    Promise<Pick<Session, "codec" | "definitions">>
  >();
  const headers = (token: string) => ({
    Accept: pilType,
    "PIL-Version": "2",
    "Trunk-Completion": "body",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  });
  async function session(tap: string, token: string, storage = archive) {
    const key = JSON.stringify([tap, token]);
    let value = sessions.get(key);
    if (!value) {
      let shared = credentials.get(token);
      if (!shared) {
        shared = modulePromise.then(async (module) => {
          const codec = new Codec(await WebAssembly.instantiate(module));
          const definitions = new Map<string, StoredDefinition>();
          if (storage) {
            const saved = await storage(token, { kind: "definitions" });
            for (const m of saved.definitions ?? [])
              definitions.set(m.digest, {
                ...m,
                handle: codec.define(m.pil),
                definition: cbor(m.pil) as PILDefinition,
              });
          }
          return { codec, definitions };
        });
        credentials.set(token, shared);
        shared.catch(() => credentials.delete(token));
        if (credentials.size > 8)
          credentials.delete(credentials.keys().next().value!);
      }
      value = shared.then((shared) => ({
        ...shared,
        bindings: [],
        negotiation: "",
      }));
      sessions.set(key, value);
      value.catch(() => sessions.delete(key));
    } else {
      sessions.delete(key);
      sessions.set(key, value);
    }
    if (sessions.size > 8) sessions.delete(sessions.keys().next().value!);
    return value;
  }
  async function negotiate(
    s: Session,
    tap: string,
    token: string,
    storage = archive,
  ) {
    if (s.negotiation) return;
    if (!s.loading)
      s.loading = (async () => {
        const response = await fetcher("/schemas", {
          method: "POST",
          headers: { ...headers(token), "Content-Type": "application/json" },
          body: JSON.stringify({ tap, known: [...s.definitions.keys()] }),
          signal: AbortSignal.timeout(30000),
          cache: "no-store",
        });
        const bindings: Binding[] = [];
        await consume(
          response,
          new PILStream(
            s.codec,
            s.definitions,
            () => {},
            (b) => bindings.push(b),
            async () => {
              throw Error("Unexpected event in schema response");
            },
          ),
          AbortSignal.timeout(30000),
        );
        const negotiation = response.headers.get("Definition-Version");
        if (!negotiation || !/^[a-f0-9]{64}$/.test(negotiation))
          throw Error("Missing schema negotiation token");
        s.bindings = bindings;
        if (storage)
          await storage(token, {
            kind: "remember",
            definitions: metadata(s),
            bindings,
          });
        s.negotiation = negotiation;
      })().finally(() => (s.loading = undefined));
    await s.loading;
  }
  function metadata(s: Session): Metadata[] {
    return [...new Set(s.bindings.map((b) => b.digest))].map((d) => {
      const { digest, store, pil } = s.definitions.get(d)!;
      return { digest, store, pil };
    });
  }
  function connect(port: MessagePort, storage = archive) {
    const archive = storage;
    const storedCatalogs = new Set<string>();
    const catalogKey = (token: string, b: Binding) =>
      JSON.stringify([token, b.tap, b.digest]);
    const jobs = new Map<
      number,
      { abort: AbortController; ack?: () => void }
    >();
    const send = (reply: Reply, transfer: Transferable[] = []) =>
      port.postMessage(reply, transfer);
    const cancel = (id: number) => {
      const job = jobs.get(id);
      job?.abort.abort();
      job?.ack?.();
      jobs.delete(id);
    };
    port.onmessage = (event: MessageEvent<Request>) => {
      const request = event.data;
      if (request.kind === "close") {
        for (const id of jobs.keys()) cancel(id);
        archive?.close?.();
        port.close();
        return;
      }
      if (request.kind === "cancel") {
        cancel(request.id);
        return;
      }
      if (request.kind === "ack") {
        jobs.get(request.id)?.ack?.();
        return;
      }
      if (jobs.has(request.id)) {
        send({
          id: request.id,
          kind: "error",
          message: "Duplicate worker request",
        });
        return;
      }
      const job = {
        abort: new AbortController(),
        ack: undefined as (() => void) | undefined,
      };
      jobs.set(request.id, job);
      const { signal } = job.abort,
        id = request.id;
      void (async () => {
        const token = request.token.trim();
        if (request.kind === "begin") {
          if (!archive) throw Error("Local event storage is unavailable");
          await archive(token, {
            kind: "begin",
            collection: request.collection,
          });
          return;
        }
        if (request.kind === "restore") {
          if (!archive) return;
          const restored = await archive(token, {
            kind: "restore",
            collection: request.collection,
          });
          if (!restored.collection) return;
          const cursor = restored.cursor!;
          try {
            signal.throwIfAborted();
            send({
              id,
              kind: "collection",
              collection: restored.collection,
              coverage: restored.coverage ?? [],
            });
            send({
              id,
              kind: "schemas",
              definitions: restored.definitions!,
              bindings: restored.bindings!,
            });
            send({ id, kind: "connected", headers: [] });
            for (const binding of restored.bindings!)
              send({ id, kind: "binding", binding });
            // Reuse persisted definitions in later schema negotiation too.
            const cached = await session("", token, archive);
            for (const metadata of restored.definitions!)
              if (!cached.definitions.has(metadata.digest))
                cached.definitions.set(metadata.digest, {
                  ...metadata,
                  handle: cached.codec.define(metadata.pil),
                  definition: cbor(metadata.pil) as PILDefinition,
                });
            for (;;) {
              signal.throwIfAborted();
              const next = await archive(token, { kind: "next", cursor });
              signal.throwIfAborted();
              if (next.batch?.rows.length) {
                const ack = new Promise<void>((resolve) => (job.ack = resolve));
                send({ id, kind: "batch", batch: next.batch }, [
                  next.batch.bytes,
                  next.batch.rows.buffer as ArrayBuffer,
                ]);
                await ack;
                job.ack = undefined;
              }
              if (next.done) break;
            }
          } finally {
            await archive(token, { kind: "release", cursor });
          }
          return;
        }
        const tap =
          request.kind === "schemas" ? request.tap : request.selection.tap;
        const s = await session(tap, token, archive);
        if (request.kind === "schemas") {
          await negotiate(s, tap, token, archive);
          signal.throwIfAborted();
          send({
            id,
            kind: "schemas",
            definitions: metadata(s),
            bindings: s.bindings,
          });
          return;
        }
        signal.throwIfAborted();
        send({
          id,
          kind: "schemas",
          definitions: metadata(s),
          bindings: s.bindings,
        });
        const sentDefinitions = new Set(metadata(s).map((m) => m.digest));
        const started = Date.now();
        let selections = [request.selection];
        let planned = false;
        if (archive && request.collection) {
          const local = await archive(token, {
            kind: "lookup",
            collection: request.collection,
            selection: request.selection,
            started,
          });
          if (local.cursor && local.gaps) {
            planned = true;
            const cursor = local.cursor;
            try {
              signal.throwIfAborted();
              for (const m of local.definitions!) {
                if (!s.definitions.has(m.digest))
                  s.definitions.set(m.digest, {
                    ...m,
                    handle: s.codec.define(m.pil),
                    definition: cbor(m.pil) as PILDefinition,
                  });
                send({ id, kind: "definition", definition: m });
                sentDefinitions.add(m.digest);
              }
              send({
                id,
                kind: "schemas",
                definitions: local.definitions!,
                bindings: local.bindings!,
              });
              send({ id, kind: "connected", headers: [] });
              for (const binding of local.bindings!)
                send({ id, kind: "binding", binding });
              for (;;) {
                signal.throwIfAborted();
                const next = await archive(token, { kind: "next", cursor });
                signal.throwIfAborted();
                if (next.batch?.rows.length) {
                  const ack = new Promise<void>(
                    (resolve) => (job.ack = resolve),
                  );
                  send({ id, kind: "batch", batch: next.batch }, [
                    next.batch.bytes,
                    next.batch.rows.buffer as ArrayBuffer,
                  ]);
                  await ack;
                  job.ack = undefined;
                }
                if (next.done) break;
              }
              selections = local.gaps.map((gap) => ({
                ...request.selection,
                from: new Date(gap.from).toISOString(),
                to: new Date(gap.to).toISOString(),
              }));
            } finally {
              await archive(token, { kind: "release", cursor });
            }
          }
        }
        for (const selection of selections) {
          signal.throwIfAborted();
          const response = await fetcher(collectionURL(selection), {
            method: "GET",
            headers: {
              ...headers(token),
              "Definition-Version": s.negotiation,
              "Definition-Known": [...s.definitions.keys()]
                .slice(-128)
                .join(","),
            },
            signal,
            cache: "no-store",
          });
          await checkResponse(response);
          const version = response.headers.get("Definition-Version");
          if (!version || !/^[a-f0-9]{64}$/.test(version))
            throw Error("Missing definition version");
          send({
            id,
            kind: "connected",
            headers: (response.headers.get("Trunk-Entry-Headers") ?? "")
              .split(",")
              .map((s) => s.trim())
              .filter(Boolean),
          });
          const bindings: Binding[] = [];
          const activeBindings = new Map<number, Binding>();
          await consume(
            response,
            new PILStream(
              s.codec,
              s.definitions,
              (m) => {
                sentDefinitions.add(m.digest);
                send({ id, kind: "definition", definition: m });
              },
              (binding) => {
                if (!sentDefinitions.has(binding.digest)) {
                  const { digest, store, pil } = s.definitions.get(
                    binding.digest,
                  )!;
                  send({
                    id,
                    kind: "definition",
                    definition: { digest, store, pil },
                  });
                  sentDefinitions.add(binding.digest);
                }
                bindings.push(binding);
                activeBindings.set(binding.stream, binding);
                if (
                  !s.bindings.some(
                    (b) => b.tap === binding.tap && b.digest === binding.digest,
                  )
                )
                  s.bindings.push(binding);
                send({ id, kind: "binding", binding });
              },
              async (batch) => {
                signal.throwIfAborted();
                if (archive && request.collection) {
                  const used = [
                    ...new Set(
                      Array.from(batch.rows).filter((_, i) => i % 3 === 0),
                    ),
                  ].map((stream) => activeBindings.get(stream)!);
                  const definitions = [
                    ...new Set(
                      used
                        .filter(
                          (b) => !storedCatalogs.has(catalogKey(token, b)),
                        )
                        .map((b) => b.digest),
                    ),
                  ].map((d) => {
                    const { digest, store, pil } = s.definitions.get(d)!;
                    return { digest, store, pil };
                  });
                  const saved = await archive(token, {
                    kind: "save",
                    collection: request.collection,
                    definitions,
                    bindings: used,
                    batch,
                  });
                  for (const b of used)
                    storedCatalogs.add(catalogKey(token, b));
                  batch = saved.batch!;
                  signal.throwIfAborted();
                }
                const ack = new Promise<void>((resolve) => (job.ack = resolve));
                send({ id, kind: "batch", batch }, [
                  batch.bytes,
                  batch.rows.buffer as ArrayBuffer,
                ]);
                await ack;
                job.ack = undefined;
                signal.throwIfAborted();
              },
            ),
            signal,
          );
          if (archive && request.collection)
            await archive(token, {
              kind: "finish",
              collection: request.collection,
              selection,
              started,
            });
          // Only a fully received response can certify this cached version. If a
          // follow is canceled midway, its definitions remain reusable but the next
          // collect conservatively negotiates again when the catalog has changed.
          if (version !== s.negotiation)
            s.bindings = [
              ...new Map(
                bindings.map((b) => [JSON.stringify([b.tap, b.digest]), b]),
              ).values(),
            ];
          if (archive)
            await archive(token, {
              kind: "remember",
              definitions: metadata(s),
              bindings: s.bindings,
            });
          s.negotiation = version;
          send({
            id,
            kind: "schemas",
            definitions: metadata(s),
            bindings: s.bindings,
          });
        }
        // Every gap completed and every reused interval was already certified.
        // This also gives a cache-only collection its own completion metadata.
        if (planned && archive && request.collection) {
          signal.throwIfAborted();
          await archive(token, {
            kind: "finish",
            collection: request.collection,
            selection: request.selection,
            started,
          });
        }
      })()
        .then(
          () => {
            if (!signal.aborted) send({ id, kind: "done" });
          },
          (error) => {
            if (!signal.aborted)
              send({
                id,
                kind: "error",
                message: error instanceof Error ? error.message : String(error),
              });
          },
        )
        .finally(() => {
          job.abort.abort();
          if (jobs.get(id) === job) jobs.delete(id);
        });
    };
    port.start();
  }

  return connect;
}
