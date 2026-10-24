import type { Selection } from "../events.ts";

// Only explicit equality controls are evaluated locally. Other expressions keep
// their existing server path until the query planner supports them deliberately.
export function conditions(selection: Selection): [string, string][] | undefined {
  if (!selection.where.length) return [];
  if (selection.where.length !== 1) return undefined;
  let rest = selection.where[0].trim();
  const out: [string, string][] = [];
  while (rest) {
    const match = /^(level|service|resource|operation)==("(?:[^"\\]|\\.)*")(?:\s+|$)/.exec(rest);
    if (!match) return undefined;
    out.push([match[1], JSON.parse(match[2])]);
    rest = rest.slice(match[0].length);
  }
  return out;
}
