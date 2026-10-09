import { valueAt, jsonText, type LogEvent } from "./dictionary.ts";

export const dictionaryJSONL = "application/vnd.trie-trunk.dictionary+jsonl";

export type { LogEvent } from "./dictionary.ts";

export async function listTaps(
  token: string,
  signal: AbortSignal,
): Promise<string[]> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (token.trim()) headers.Authorization = `Bearer ${token.trim()}`;
  const response = await fetch("/taps", { headers, signal, cache: "no-store" });
  await checkResponse(response);
  if (
    response.headers.get("Content-Type")?.split(";", 1)[0].trim() !==
    "application/json"
  )
    throw new Error("No tap listing at this address.");
  const value = await response.json();
  if (
    !value ||
    !Array.isArray(value.taps) ||
    !value.taps.every(
      (tap: unknown) =>
        typeof tap === "string" && /^[a-z0-9][a-z0-9._-]{0,63}$/.test(tap),
    )
  )
    throw new Error("Invalid tap listing.");
  return value.taps;
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
      "Authentication required. Add your bearer token in Connection settings.",
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
  if (value == null) return "";
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
