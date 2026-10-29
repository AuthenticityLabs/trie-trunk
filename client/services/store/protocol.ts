import type { Metadata, Binding } from "../pil/metadata.ts";
import type { Batch, NativeRecord } from "../pil/stream.ts";
import type { Selection } from "../events.ts";
import type { Parameters } from "../search.ts";
import type { NativePage, NativeSource } from "./native.ts";
import type { ObjectQuery } from "../replay.ts";
export type Collection = {
  id: string;
  created: number;
  selection: Selection;
  parameters: Parameters;
  range: string;
  from: string;
  to: string;
};
export type NativeSelection = {
  source: string;
  from: number;
  to: number;
  where: [string, string][];
};
export type NativeInput = {record:NativeRecord;definition?:Metadata};
export type Command =
  | {kind:'summary-read';query:import('./native.ts').SummaryQuery}
  | {kind:'summary-save';input:import('./native.ts').SummaryImport}
  | {kind:'time-read';query:import('./native.ts').TimeQuery}
  | {kind:'time-save';input:import('./native.ts').TimeImport}
  | {kind:'trunk-save';snapshot:import('../pil/trunk.ts').TrunkSnapshot}
  | { kind: "native-rebase"; sources:string[] }
  | { kind: "native-begin"; session: string }
  | { kind: "native-records"; session:string; records:NativeInput[] }
  | { kind: "native-resolve"; tap:string; digest:string; generation:string; from:number; to:number }
  | { kind: "native-record"; session: string; record: NativeRecord; definition?: Metadata }
  | { kind: "native-finish"; session: string; records?:NativeInput[]; collection?: string; selection?: Selection }
  | { kind: "native-abort"; session: string }
  | { kind: "native-coverage"; tap?: string }
  | { kind: "native-object-state"; query: ObjectQuery }
  | { kind: "native-dependencies"; source: string }
  | { kind: "native-rows"; source: string; from: number; to: number; rows: number[] }
  | { kind: "native-page"; source: string; from: number; to: number; after?: number; limit?: number; descending?:boolean }
  | { kind: "definitions" }
  | { kind: "remember"; definitions: Metadata[]; bindings: Binding[] }
  | { kind: "begin"; collection: Collection }
  | {
      kind: "save";
      collection: string;
      definitions: Metadata[];
      bindings: Binding[];
      batch: Batch;
    }
  | {
      kind: "finish";
      collection: string;
      selection: Selection;
      started: number;
    }
  | {
      kind: "lookup";
      collection: string;
      selection: Selection;
      started: number;
    }
  | { kind: "restore"; collection?: string }
  | { kind: "next"; cursor: string }
  | { kind: "release"; cursor: string };
export type ArchiveReply = {
  summaryBytes?:Uint8Array;
  time?:import('./native.ts').TimeReceipt;
  nativeObjectState?: { token:string; sequence:number };
  nativeSelections?: NativeSelection[];
  nativeSources?: string[];
  nativePage?: NativePage;
  nativeDependencies?: { source: NativeSource; definition: Metadata; trie: {start:number;bytes:Uint8Array}[] };
  nativeCoverage?: { Definition:number[]; Generation:number[]; Index:{Start:number;End:number}[]; MemberRanges:{Start:number;End:number}[]; TrieNodes:number; StateKnown?:boolean; StateEpoch?:number[]; StateSequence?:number }[];
  batch?: Batch;
  definitions?: Metadata[];
  bindings?: Binding[];
  collection?: Collection;
  coverage?: { from: number; to: number }[];
  cursor?: string;
  gaps?: { from: number; to: number }[];
  done?: boolean;
};
export type Archive = ((
  token: string,
  command: Command,
) => Promise<ArchiveReply>) & { close?: () => void };
