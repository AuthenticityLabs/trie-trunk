const branches: Record<string, string> = {
  read: 'Info - Read',
  write: 'Info - Write',
  error: 'Error',
  debug: 'Debug',
  trace: 'Trace',
};
const branchOrder = ['read', 'write', 'error', 'debug', 'trace'];

// Display names never become field values, physical filenames, or quota keys.
export function storageNodeName(path: string[]): string {
  const name = path.at(-1);
  return path.length === 4 ? branches[name!] ?? name! : name ?? 'Trunk';
}
export function storageScopeLabel(path: string[]): string {
  return path.length ? path.map((_, index) => storageNodeName(path.slice(0, index + 1))).join(' / ') : 'Trunk';
}
export function compareStoragePaths(a: string[], b: string[]): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    if (a[i] === b[i]) continue;
    if (i === 3 && branchOrder.includes(a[i]) && branchOrder.includes(b[i])) return branchOrder.indexOf(a[i]) - branchOrder.indexOf(b[i]);
    return a[i].localeCompare(b[i]);
  }
  return a.length - b.length;
}
