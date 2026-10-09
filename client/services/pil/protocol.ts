import type { Collection } from "../store/protocol.ts";
import type { Selection } from "../events.ts";
import type { Metadata, Binding } from "./metadata.ts";
import type { Batch } from "./stream.ts";
export type Request =
  | { id: number; kind: "schemas"; tap: string; token: string }
  | {
      id: number;
      kind: "events";
      selection: Selection;
      token: string;
      collection?: string;
    }
  | { id: number; kind: "begin"; collection: Collection; token: string }
  | { id: number; kind: "restore"; collection?: string; token: string }
  | { id: number; kind: "cancel" }
  | { id: number; kind: "ack" }
  | { kind: "close" };
export type Reply =
  | {
      id: number;
      kind: "collection";
      collection: Collection;
      coverage: { from: number; to: number }[];
    }
  | {
      id: number;
      kind: "schemas";
      definitions: Metadata[];
      bindings: Binding[];
    }
  | { id: number; kind: "definition"; definition: Metadata }
  | { id: number; kind: "binding"; binding: Binding }
  | { id: number; kind: "batch"; batch: Batch }
  | { id: number; kind: "connected"; headers: string[] }
  | { id: number; kind: "done" }
  | { id: number; kind: "error"; message: string };
