import type { FileIO } from "./wasm.ts";
import { storageError, storageStep } from './io-error.ts';
export async function directory(
  root: FileSystemDirectoryHandle,
  ...names: string[]
) {
  for (const name of names)
    root = await storageStep(`Open Local Folder ${name}`, () => root.getDirectoryHandle(name, { create: true }));
  return root;
}
export async function handle(
  root: FileSystemDirectoryHandle,
  name: string,
): Promise<FileIO> {
  const file = await storageStep(`Open Local File ${name}`, () => root.getFileHandle(name, { create: true }));
  if (!("createSyncAccessHandle" in file))
    throw Error("This browser does not support local event files");
  const io = await storageStep(`Open Local File Access ${name}`, () => (
    file as FileSystemFileHandle & { createSyncAccessHandle(): Promise<FileIO> }
  ).createSyncAccessHandle());
  return describeFileIO(io, name);
}
export function describeFileIO(io: FileIO, name: string): FileIO {
  const run = <T>(operation: string, work: () => T): T => {
    try { return work(); } catch (error) { throw storageError(`${operation} ${name}`, error); }
  };
  return {
    getSize: () => run('Measure Local File', () => io.getSize()),
    read: (bytes, options) => run('Read Local File', () => io.read(bytes, options)),
    write: (bytes, options) => run(`Write Local File (${bytes.length} bytes at ${options.at})`, () => io.write(bytes, options)),
    truncate: size => run(`Resize Local File (${size} bytes)`, () => io.truncate(size)),
    flush: () => run('Flush Local File', () => io.flush()),
    close: () => run('Close Local File', () => io.close()),
  };
}
export function write(h: FileIO, b: Uint8Array, at: number) {
  let n = 0;
  while (n < b.length) {
    const wrote = h.write(b.subarray(n), { at: at + n });
    if (wrote <= 0) throw Error("Could not write local event file");
    n += wrote;
  }
}
export function readFile(h: FileIO, at = 0, length = h.getSize() - at) {
  const out = new Uint8Array(length);
  let n = 0;
  while (n < length) {
    const count = h.read(out.subarray(n), { at: at + n });
    if (count <= 0) throw Error("Incomplete local event file");
    n += count;
  }
  return out;
}
export async function bytes(root: FileSystemDirectoryHandle, name: string) {
  const h = await handle(root, name);
  try {
    return readFile(h);
  } finally {
    h.close();
  }
}
export async function immutable(
  root: FileSystemDirectoryHandle,
  name: string,
  b: Uint8Array,
) {
  const h = await handle(root, name);
  try {
    if (h.getSize()) {
      const old = readFile(h);
      if (old.length !== b.length || old.some((v, i) => v !== b[i]))
        throw Error("Conflicting local definition");
    } else {
      write(h, b, 0);
    }
    h.flush();
  } finally {
    h.close();
  }
}
export function entries(root: FileSystemDirectoryHandle) {
  return (
    root as FileSystemDirectoryHandle & {
      values(): AsyncIterableIterator<FileSystemHandle>;
    }
  ).values();
}
export function recover(file: FileIO, committed: number | undefined) {
  const end = committed ?? 0;
  if (!Number.isSafeInteger(end) || end < 0 || file.getSize() < end)
    throw Error("Committed event file is missing or truncated");
  if (file.getSize() > end) {
    file.truncate(end);
    file.flush();
  }
}
