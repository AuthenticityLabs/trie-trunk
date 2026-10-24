import type { QuerySyntax } from './query.ts';

export const querySyntaxKey = 'snoot.querySyntax';
export function querySyntaxPreference(value: unknown): QuerySyntax {
  return value === 'pipeline' ? 'pipeline' : 'builder';
}
export function loadQuerySyntax(): QuerySyntax {
  try { return querySyntaxPreference(localStorage.getItem(querySyntaxKey)); }
  catch { return 'builder'; }
}
export function saveQuerySyntax(value: QuerySyntax): boolean {
  try { localStorage.setItem(querySyntaxKey, value); return true; }
  catch { return false; }
}
