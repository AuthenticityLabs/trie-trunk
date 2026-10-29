import { rawValue, valueAt, type LogEvent } from './dictionary.ts';
import { literal, type Filter } from './query.ts';
import { validSource } from './source.ts';
import type { ViewQuery } from './pil/view.ts';

export type DiagnosticKind = 'debug' | 'trace';
export type DiagnosticTarget = {
  kind: DiagnosticKind;
  source: string;
  linkedBy: string;
  value: string;
  query: ViewQuery;
};

export function isDiagnostic(event: LogEvent): boolean {
  const parts = event.dictionary.tap.split('/');
  if (parts.length === 4) return parts[3] === 'debug' || parts[3] === 'trace';
  // Older streams do not have a physical branch in their source binding.
  const level = valueAt(event, 'level');
  return typeof level === 'string' && ['DEBUG', 'TRACE'].includes(level.toUpperCase());
}

export function diagnosticTargets(event: LogEvent, sources: readonly string[], window?: {from:string;to:string}): DiagnosticTarget[] {
  const tap = event.dictionary.tap;
  if (!validSource(tap) || tap.split('/').length !== 4 || isDiagnostic(event)) return [];
  const resource = tap.split('/').slice(0, 3).join('/');
  return (['debug', 'trace'] as const).flatMap(kind => {
    const source = `${resource}/${kind}`;
    if (!sources.includes(source)) return [];
    let linkedBy = 'timestamp', value = new Date(event.millis).toISOString();
    let from = value, to = new Date(event.millis + 1).toISOString();
    const filters: Filter[] = [];
    // Prefer the developer's explicit link. Timestamp matching is exact to the
    // event's millisecond and remains available without correlation fields.
    for (const name of kind === 'trace' ? ['trace_id', 'correlation_id'] : ['correlation_id', 'trace_id']) {
      const field = event.dictionary.fields.names.get(name);
      const raw = rawValue(event, field);
      if (raw === undefined || raw === null || raw === '' || raw instanceof Uint8Array && !field?.uuid) continue;
      const decoded = valueAt(event, name);
      if (decoded === '') continue;
      if (typeof decoded !== 'string' && typeof decoded !== 'number' && typeof decoded !== 'bigint') continue;
      try {
        filters.push({field:name,op:'==',value:literal(decoded)});
      } catch { continue; }
      linkedBy = name; value = String(decoded);
      // Correlations can span multiple timestamps. Bound these lookups to the
      // selected search window, or retained history when no window is supplied.
      const a = window ? Date.parse(window.from) : NaN, z = window ? Date.parse(window.to) : NaN;
      from = Number.isFinite(a) && a <= event.millis ? window!.from : '1970-01-01T00:00:00Z';
      to = Number.isFinite(z) && z > event.millis ? window!.to : new Date(Math.max(Date.now(), event.millis + 1)).toISOString();
      break;
    }
    return [{kind,source,linkedBy,value,query:{tap:source,from,to,size:50,include:'page',query:{filters,order:'asc'}}}];
  });
}
