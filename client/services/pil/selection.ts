import {resourceURL,timeParameters} from '../resources.ts';
import type { Selection } from "../events.ts";

export function collectionURL(selection: Selection): string {
  const query = new URLSearchParams({
    tap: selection.tap,
    from: selection.from,
    limit: selection.limit === null ? "all" : String(selection.limit),
    tail: String(selection.tail),
    follow: String(selection.follow),
  });
  if (selection.to) query.set("to", selection.to);
  for (const clause of selection.where) query.append("where", clause);
  query.delete('tap');query.delete('from');query.delete('to');for(const [k,v] of timeParameters(selection.from,selection.to||undefined))query.set(k,v);
  return resourceURL('seeds',selection.tap,query);
}
