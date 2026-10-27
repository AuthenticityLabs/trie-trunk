import type { QuerySyntax } from './query.ts';

export const querySyntaxKey = 'snoot:search:syntax';
export function querySyntaxPreference(value: unknown): QuerySyntax {
  return value === 'builder' ? 'builder' : 'pipeline';
}
export function loadQuerySyntax(): QuerySyntax {
  try { return querySyntaxPreference(sessionStorage.getItem(querySyntaxKey)); }
  catch { return 'pipeline'; }
}
export function saveQuerySyntax(value: QuerySyntax): boolean {
  try { sessionStorage.setItem(querySyntaxKey, value); return true; }
  catch { return false; }
}
