import type { ViewQuery, ViewResponse } from "./view.ts";
import type { Collection } from "../store/protocol.ts";
import type { Selection } from "../events.ts";
import type { Metadata, Binding } from "./metadata.ts";
import type { Batch } from "./stream.ts";
import type { NativePage } from "../store/native.ts";
import type { HistoryQuery, ObservedState } from "../replay.ts";
export type Request =
  | { id:number; kind:"fast-dashboard"; request:import('../fast-dashboard.ts').FastRequest; token:string }
  | { id:number; kind:"prime-summaries"; service:string; recent:boolean; token:string }
  | { id:number; kind:"persist-view"; definitions:Metadata[]; bindings:Binding[]; token:string }
  | { id:number; kind:"remember-view"; view:ViewResponse; token:string }
  | { id:number; kind:"view"; query:ViewQuery; token:string; progressive?:boolean }
  | { id:number; kind:"rebase"; sources:string[]; token:string }
  | { id: number; kind: "history"; query: HistoryQuery; token: string }
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
  | { id:number; kind:"view-page"; view:ViewResponse }
  | { id:number; kind:"view-summary"; result:ViewResponse["result"]; timings:ViewResponse["timings"] }
  | { id:number; kind:"fast-dashboard"; result:import('../fast-dashboard.ts').FastResult }
  | { id:number; kind:"summary-progress"; progress:import("../summary-replication.ts").SummaryProgress }
  | { id:number; kind:"view"; view:ViewResponse }
  | { id: number; kind: "history-state"; state: ObservedState }
  | { id:number; kind:"native-source"; source:string; definition:Metadata; binding:Binding; trie:{start:number;bytes:Uint8Array}[] }
  | { id:number; kind:"native-batch"; source:string; page:NativePage; where:[string,string][] }
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
  | { id: number; kind: "error"; message: string; cacheIssue?:import("../store/errors").NativeCacheIssue };
