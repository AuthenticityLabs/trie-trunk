// Legacy tap names and explicit resource branch bindings. These are
// catalog identities, never filesystem paths supplied to OPFS.
export function validSource(value: unknown): value is string {
  if (typeof value !== "string") return false;
  if (/^[a-z0-9][a-z0-9._-]{0,63}$/.test(value)) return true;
  const parts = value.split("/");
  return parts.length === 4 && parts.slice(0, 3).every(p => /^[a-z][a-z0-9._-]{0,127}$/.test(p)) &&
    /\.v[1-9][0-9]*$/.test(parts[2]) && /^(read|write|error|debug|trace)$/.test(parts[3]);
}

// Search scope stops at the resource. Level and access are independent event fields.
export const sourceParts = ["cache", "service", "resource"] as const;
export type SourceScope = Partial<Record<(typeof sourceParts)[number], string>>;

// Catalog selectors, never physical paths. Wildcards match a whole component;
// actual stream bindings remain exact identities.
export function sourceScope(value: string): SourceScope {
  if (!value) return {};
  const parts = value.split("/");
  if (parts.length === 1) return { cache: value };
  return Object.fromEntries(sourceParts.map((key, i) => [key, parts[i] === "*" ? "" : parts[i]]));
}

export function scopeSource(scope: SourceScope, catalog: readonly string[]): string {
  if (!sourceParts.some(key => scope[key])) return "";
  if (scope.cache && catalog.includes(scope.cache) && !catalog.some(s => s.startsWith(`${scope.cache}/`)))
    return scope.cache; // Older servers advertise simple tap identities.
  return [...sourceParts.map(key => scope[key] || "*"), "*"].join("/");
}

export function matchesSource(scope: string, source: string): boolean {
  if (!scope || scope === source) return true;
  const wanted = scope.split("/"), actual = source.split("/");
  return wanted.length === 4 && actual.length === 4 &&
    wanted.every((part, index) => part === "*" || part === actual[index]);
}

export function sourceOptions(catalog: readonly string[], scope: SourceScope, index: number): string[] {
  return [...new Set(catalog.flatMap(source => {
    const parts = source.split("/");
    if (!sourceParts.slice(0, index).every((key, i) => !scope[key] || scope[key] === parts[i])) return [];
    return parts[index] ? [parts[index]] : [];
  }))].sort();
}

// A restored search must not retain an invisible physical-partition constraint.
export function resourceScope(value: string): string {
  const parts=value.split('/');
  return parts.length===4 ? [...parts.slice(0,3),'*'].join('/') : value;
}
