import { For, Show, createMemo, createUniqueId } from 'solid-js';
import { decodeValue, eventId, expandEvent, jsonText, rawValue, type LogEvent } from '../services/dictionary';
import { historyValue, type HistoryDiff } from '../services/history-diff';
import { eventFields, type EventField } from '../services/indicators';
import { FieldTag } from './events';
import { Icon } from './icon';

// Adapted from Authenticity's Entity Models version-rail / version-panel layout.
export function HistoryEntry(props: {
  event:LogEvent;diff:HistoryDiff;open:boolean;latest?:boolean;onToggle:()=>void;
  onIndicator:(field:EventField)=>void;indicatorsLoading:boolean;
  onSave:(anchor:HTMLElement)=>void;saved?:string;
}) {
  const key=createUniqueId();
  const revision=createMemo(()=>{
    const field=props.event.dictionary.revisionIdentity;
    const value=rawValue(props.event,field);
    return value===undefined||value===null?undefined:String(decodeValue(value,field));
  });
  const title=()=>revision()!==undefined?`Revision ${revision()}`:'Change Event';
  const stamp=()=>new Date(props.event.millis).toISOString();
  return <article class="history-entry" classList={{expanded:props.open,latest:props.latest,compared:props.open&&!props.latest}}>
    <button type="button" class="history-toggle history-rail" id={`${key}-toggle`} aria-expanded={props.open}
      aria-label={`${props.latest?'Latest ':''}${title()} · ${stamp()}`} aria-controls={`${key}-panel`} onClick={props.onToggle}>
      <span class="history-node">{revision()!==undefined?`r${revision()}`:'Event'}</span>
      <span class="history-rail-date">{props.latest?'Latest':new Intl.DateTimeFormat('en',{month:'short',day:'numeric',timeZone:'UTC'}).format(props.event.millis)}</span>
    </button>
    <Show when={props.open}>
      <section id={`${key}-panel`} class="history-panel" role="region" aria-labelledby={`${key}-toggle`}>
        <div class="history-panel-head"><span class="history-copy"><strong>{title()}</strong><small>{stamp().replace('T',' ').replace('Z',' UTC')}</small></span>
          <span class="history-change-count" title="Compared with previously observed values in this history">{props.diff.changes.length} {props.diff.changes.length===1?'change':'changes'}</span></div>
        <div class="history-diff" aria-label="Field Changes">
          <Show when={props.diff.changes.length} fallback={<p class="history-quiet">No changed values among the supplied fields.</p>}>
            <For each={props.diff.changes}>{change=><div class={`history-diff-row diff-${change.kind}`}>
              <span class="history-diff-mark" title={change.kind==='observed'?'First observed in retained history':'Changed from the previously observed value'}>{change.kind==='observed'?'·':'~'}</span>
              <strong>{change.name}</strong>
              <div class="history-diff-values"><Show when={change.kind==='changed'} fallback={<small class="history-first-seen">First Seen</small>}>
                <del>{historyValue(change.before)}</del><span class="history-diff-arrow" aria-label="Changed to">→</span>
              </Show><ins>{historyValue(change.after)}</ins></div>
            </div>}</For>
          </Show>
        </div>
        <div class="history-entry-meta"><span>Event ID <code>{eventId(props.event)}</code></span>
          <button type="button" class="icon-button" aria-label={`Save Event ${eventId(props.event)} to Notebook`} onClick={e=>props.onSave(e.currentTarget)}><Icon name="notebook"/></button>
          <Show when={props.saved}><small role="status">Saved to {props.saved}</small></Show>
        </div>
        <details class="history-raw"><summary>Supplied Fields · {props.diff.supplied}</summary>
          <div class="event-fields"><For each={eventFields(props.event)}>{field=><FieldTag field={field} disabled={props.indicatorsLoading} onIndicator={props.onIndicator}/>}</For></div>
        </details>
        <details class="history-raw"><summary>Event JSON</summary><pre>{jsonText(expandEvent(props.event),2)}</pre></details>
      </section>
    </Show>
  </article>;
}
