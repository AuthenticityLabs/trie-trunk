import type { Metadata, Binding } from "../pil/metadata.ts";
import type { Batch } from "../pil/stream.ts";
import type { Selection } from "../events.ts";
import type { Parameters } from "../search.ts";
export type Collection = {
  id: string;
  created: number;
  selection: Selection;
  parameters: Parameters;
  range: string;
  from: string;
  to: string;
};
export type Command =
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
