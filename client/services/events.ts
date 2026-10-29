import {apiFetch} from './connections.ts';
import { validSource } from "./source.ts";
import { valueAt, jsonText, type LogEvent } from "./dictionary.ts";

export const dictionaryJSONL = "application/vnd.trie-trunk.dictionary+jsonl";

export type { LogEvent } from "./dictionary.ts";

export async function listCaches(
  token: string,
  signal: AbortSignal,
  collections?: (sources:string[])=>void,
): Promise<string[]> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (token.trim()) headers.Authorization = `Bearer ${token.trim()}`;
  const response = await apiFetch("/caches", { headers, signal, cache: "no-store" });
  await checkResponse(response);
  if (
    response.headers.get("Content-Type")?.split(";", 1)[0].trim() !==
    "application/json"
  )
    throw new Error("No cache listing at this address.");
  const value = await response.json();
  if (
    !value ||
    !Array.isArray(value.caches) ||
    !value.caches.every(validSource)
  )
    throw new Error("Invalid cache listing.");
  const replay=value.collections??[];
  if(!Array.isArray(replay)||!replay.every((s:unknown)=>validSource(s)&&s.endsWith('/write')&&value.caches.includes(s)))
    throw new Error("Invalid collection capabilities.");
  collections?.(replay);
  return value.caches;
}

export type Selection = {
  tap: string;
  from: string;
  to?: string;
  where: string[];
  limit: number | null;
  tail: boolean;
  follow: boolean;
  pretty: false;
};

export { loadDictionaries, streamEvents } from "./pil/client.ts";

async function checkResponse(response: Response) {
  if (response.ok) return;
  if (response.status === 401)
    throw new Error(
      "Authentication required. Add your bearer token in Profile → Connections.",
    );
  if (response.status === 503)
    throw new Error(
      "The server is busy. Stop another collection or try again shortly.",
    );
  throw new Error(
    (await response.text()).trim() || `Request failed (${response.status}).`,
  );
}

export function field(event: LogEvent, name: string): string {
  const value = valueAt(event, name);
  if (value == null) {
    // Classification moved into the source hierarchy; render its binding at the
    // view edge without adding synthetic fields to the PIL object.
    const source=event.dictionary.tap.split("/");
    if(source.length===4 && !event.dictionary.fields.names.has(name)) {
      if(name==="service")return source[1];
      if(name==="resource")return source[2];
    }
    return "";
  }
  return typeof value === "object" ? jsonText(value) : String(value);
}

export function level(event: LogEvent): string {
  return field(event, "level").toUpperCase() || "UNKNOWN";
}

export function duration(value: unknown): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "—";
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)} s`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(2)} ms`;
  return `${value} µs`;
}
