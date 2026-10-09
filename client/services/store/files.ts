import type { FileIO } from "./wasm.ts";
export async function directory(
  root: FileSystemDirectoryHandle,
  ...names: string[]
) {
  for (const name of names)
    root = await root.getDirectoryHandle(name, { create: true });
  return root;
}
export async function handle(
  root: FileSystemDirectoryHandle,
  name: string,
): Promise<FileIO> {
  const file = await root.getFileHandle(name, { create: true });
  if (!("createSyncAccessHandle" in file))
    throw Error("This browser does not support local event files");
  return (
    file as FileSystemFileHandle & { createSyncAccessHandle(): Promise<FileIO> }
  ).createSyncAccessHandle();
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
      h.flush();
    }
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
