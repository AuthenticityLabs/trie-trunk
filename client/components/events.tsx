import { RowWindow } from '../services/row-window';
import { capturePosition, resolvePosition, type ScrollPosition } from '../services/ui-position';
import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, onMount } from "solid-js";
import { Dynamic } from "solid-js/web";
import { duration, field, level, type LogEvent } from "../services/events";
import { Icon } from "./icon";
import { NotebookPicker } from "./notebook";
import {
  eventId,
  expandEvent,
  valueAt,
  jsonText,
} from "../services/dictionary";
import { ScrollEnd } from "./scroll-end";
import { objectHistoryTarget, type ObjectHistoryTarget } from '../services/replay';
import { diagnosticTargets, type DiagnosticTarget } from '../services/diagnostics';
import {
  eventFields,
  type Annotations,
  type EventField,
} from "../services/indicators";

const clocks=new Map<string,Intl.DateTimeFormat>();
export function eventTime(event:LogEvent,zone='UTC'):string {
 let clock=clocks.get(zone);if(!clock){clock=new Intl.DateTimeFormat('en-GB',{hour:'2-digit',minute:'2-digit',second:'2-digit',fractionalSecondDigits:3,timeZone:zone});clocks.set(zone,clock)}return clock.format(event.millis);
}

export function FieldTag(props: {
  field: EventField;
  disabled: boolean;
  onIndicator: (field: EventField) => void;
  onHistory?: ()=>void;
}) {
  return (
    <Show when={props.onHistory} fallback={
    <Dynamic
      component={props.field.fixed ? "span" : "button"}
      class="field-tag"
      classList={{ fixed: props.field.fixed }}
      disabled={props.field.fixed ? undefined : props.disabled}
      aria-haspopup={props.field.fixed ? undefined : "dialog"}
      aria-label={
        props.field.fixed
          ? undefined
          : `Add Indicator for ${props.field.path.join(".")}`
      }
      title={`${props.field.path.join(".")}: ${props.field.text}${props.field.fixed ? "" : "\nAdd Indicator"}`}
      onClick={
        props.field.fixed ? undefined : () => props.onIndicator(props.field)
      }
    >
      <span class="field-name">{props.field.path.join(".")}</span>
      <span class="field-value">{props.field.text || '""'}</span>
      <Show when={!props.field.fixed}>
        <Icon name="plus" size={11} />
      </Show>
    </Dynamic>
    }>
      <span class="field-tag">
        <span class="field-name">{props.field.path.join('.')}</span>
        <button type="button" class="field-value object-history-link" aria-haspopup="dialog" onClick={()=>props.onHistory?.()}
          aria-label={`Open Object History for ${props.field.path.join('.')} ${props.field.text}`} title="Open Object History">{props.field.text||'""'}</button>
        <Show when={!props.field.fixed}><button type="button" class="field-indicator-button" disabled={props.disabled}
          aria-label={`Add Indicator for ${props.field.path.join('.')}`} aria-haspopup="dialog" onClick={()=>props.onIndicator(props.field)}><Icon name="plus" size={11}/></button></Show>
      </span>
    </Show>
  );
}

export function Events(props: {
 timeZone?:string;
  revision: number;
  events: LogEvent[];
  selected: LogEvent | undefined;
  select: (event: LogEvent) => void;
  annotations: Annotations;
  onIndicator: (field: EventField) => void;
  indicatorsLoading: boolean;
  reveal: { event: LogEvent } | undefined;
  pageSize: number;
  onNearEnd?: () => void;
  hasMore?: boolean;
  loading?: boolean;
  active?: boolean;
  position?: ScrollPosition;
  onPosition?: (position:ScrollPosition) => void;
  collections?: readonly string[];
  onObjectHistory?: (target:ObjectHistoryTarget)=>void;
  diagnosticSources?: readonly string[];
  diagnosticWindow?: {from:string;to:string};
  onDiagnostic?: (target:DiagnosticTarget)=>void;
}) {
  const [visibleCount, setVisibleCount] = createSignal(props.pageSize);
  const [notebook, setNotebook] = createSignal<{
    event: LogEvent;
    anchor: HTMLElement;
  }>();
  const [saved, setSaved] = createSignal<{ event: LogEvent; name: string }>();
  let table: HTMLDivElement | undefined;
  let scrollTimer: ReturnType<typeof setTimeout> | undefined;
  let scrollFrame = 0;
  const model = new RowWindow();
  const [measureVersion, setMeasureVersion] = createSignal(0);
  const [viewport, setViewport] = createSignal({top:0,height:590});
  const updateViewport = () => {
    if(table)setViewport({top:Math.max(0,table.scrollTop-32),height:table.clientHeight||590});
  };
  const savePosition = () => {clearTimeout(scrollTimer);if(table?.getClientRects().length)props.onPosition?.(capturePosition(table))};
  const visible = () => props.onNearEnd ? props.events : props.events.slice(0, visibleCount());
  let first: LogEvent | undefined, previousRevision: number | undefined;
  const available = createMemo(() => {
    const rows=visible(), reset=first!==rows[0]||previousRevision!==props.revision;
    model.resize(rows.length,reset);first=rows[0];previousRevision=props.revision;
    return rows;
  });
  const windowRange = createMemo(() => {
    available();measureVersion();
    return model.range(viewport().top,viewport().height);
  });
  const windowRows = createMemo(() => available().slice(windowRange().start,windowRange().end));
  const more = () => props.onNearEnd ? props.hasMore : visibleCount() < props.events.length;
  const loadMore = () => props.onNearEnd ? props.onNearEnd() : setVisibleCount(n => n + props.pageSize);
  const restorePosition = () => {
    if(table&&props.position)table.scrollTo(resolvePosition(props.position,table));
    updateViewport();
  };
  onMount(()=>{
    queueMicrotask(restorePosition);
    let width=table?.clientWidth;
    const resize=new ResizeObserver(()=>{
      if(table?.clientWidth&&width!==table.clientWidth){
        width=table.clientWidth;model.resize(available().length,true);setMeasureVersion(n=>n+1);
      }
      updateViewport();
    });
    if(table)resize.observe(table);
    addEventListener('pagehide',savePosition);
    onCleanup(()=>{resize.disconnect();cancelAnimationFrame(scrollFrame);clearTimeout(scrollTimer);removeEventListener('pagehide',savePosition)});
  });
  createEffect(on(()=>props.active,active=>{if(active)queueMicrotask(restorePosition)}));

  createEffect(
    on(
      () => props.reveal,
      (request) => {
        if (!request) return;
        const index = props.events.indexOf(request.event);
        if (index < 0) return;
        setVisibleCount(n => Math.max(n, index + props.pageSize));
        if(table){table.scrollTop=Math.max(0,model.offset(index)-table.clientHeight/2);updateViewport();}
        requestAnimationFrame(() => {
          if (props.reveal !== request) return;
          const row = table?.querySelector<HTMLTableRowElement>(
            `[data-event-row="${index}"]`,
          );
          row?.scrollIntoView({ block: "center", inline: "nearest" });
          row?.focus({ preventScroll: true });
        });
      },
    ),
  );

  createEffect(
    on(
      () => props.revision,
      () => {
        setVisibleCount(props.pageSize);
        queueMicrotask(()=>{if(table)table.scrollTo(resolvePosition(props.position ?? {top:0,left:0},table));updateViewport()});
        setNotebook(undefined);
      },
    ),
  );

  return (
    <>
      <div class="table-scroll windowed-events" ref={table} onScroll={()=>{cancelAnimationFrame(scrollFrame);scrollFrame=requestAnimationFrame(updateViewport);clearTimeout(scrollTimer);scrollTimer=setTimeout(savePosition,100)}}>
        <table class="event-table">
          <thead>
            <tr>
              <th class="expand-cell">
                <span class="sr-only">Event actions</span>
              </th>
              <th>
                Time <span>{props.timeZone??'UTC'}</span>
              </th>
              <th>Level</th>
              <th>Service</th>
              <th class="resource-cell">Resource</th>
              <th class="operation-cell">Operation</th>
              <th class="id-cell">ID</th>
              <th>Event</th>
              <th class="duration-cell">Duration</th>
            </tr>
          </thead>
          <tbody aria-hidden="true"><tr class="event-spacer"><td colSpan={9} style={{height:`${windowRange().before}px`}} /></tr></tbody>
            <For each={windowRows()}>
              {(event, index) => {
                const absoluteIndex=()=>windowRange().start+index();
                let group!: HTMLTableSectionElement;
                onMount(()=>{
                  const observer=new ResizeObserver(entries=>{
                    const height=entries[0]?.contentRect.height;
                    if(!height)return;
                    const at=absoluteIndex(),oldEnd=model.offset(at+1),delta=model.measure(at,height);
                    if(Math.abs(delta)<0.1)return;
                    if(table&&oldEnd<viewport().top)table.scrollTop+=delta;
                    setMeasureVersion(n=>n+1);updateViewport();
                  });
                  observer.observe(group);
                  onCleanup(()=>observer.disconnect());
                });
                const id = eventId(event);
                const message = field(event, "message");
                const fields = eventFields(event);
                const history=()=>objectHistoryTarget(event,props.collections??[]);
                const otherFields = fields.filter(
                  (item) =>
                    ![event.dictionary.id.name, "resource", "operation"].includes(item.path[0]),
                );
                const severity = level(event);
                return (
                  <tbody ref={group}>
                    <tr
                      class="event-summary"
                      id={`event-${id}`}
                      data-event-row={absoluteIndex()}
                      tabIndex={-1}
                      classList={{
                        selected: props.selected === event,
                        linked: props.reveal?.event === event,
                      }}
                    >
                      <td class="expand-cell">
                        <div class="event-margin">
                        <button
                          class="expand-button"
                          aria-label={`Inspect event ${id}`}
                          aria-expanded={props.selected === event}
                          onClick={() => props.select(event)}
                        >
                          <Icon
                            name={props.selected === event ? "down" : "chevron"}
                            size={14}
                          />
                        </button>
                        <Show when={props.onDiagnostic}><span class="diagnostic-leaves">
                          <For each={diagnosticTargets(event,props.diagnosticSources??[],props.diagnosticWindow)}>{target=><button
                            type="button" class={`diagnostic-leaf diagnostic-${target.kind}`} aria-haspopup="dialog"
                            aria-label={`Inspect ${target.kind} data linked by ${target.linkedBy} for event ${id}`}
                            title={`${target.kind==='debug'?'Debug':'Trace'} data · ${target.linkedBy}: ${target.value}`}
                            onClick={()=>props.onDiagnostic?.(target)}><Icon name="leaf" size={14}/><small>{target.kind==='debug'?'D':'T'}</small></button>}</For>
                        </span></Show>
                        </div>
                      </td>
                      <td class="time-cell" title={field(event, "time")}>
                        {eventTime(event,props.timeZone)}
                      </td>
                      <td>
                        <span class={`level level-${severity.toLowerCase()}`}>
                          {severity}
                        </span>
                      </td>
                      <td class="service-cell" title={field(event, "service")}>
                        {field(event, "service") || "—"}
                      </td>
                      <td
                        class="resource-cell"
                        title={field(event, "resource")}
                      >
                        {field(event, "resource") || "—"}
                      </td>
                      <td
                        class="operation-cell"
                        title={field(event, "operation")}
                      >
                        {field(event, "operation") || "—"}
                      </td>
                      <td class="id-cell" title={id}>
                        {id}
                      </td>
                      <td class="message-cell">
                        <div class="event-heading">
                          <Show
                            when={
                              message || props.annotations.get(event)?.length
                            }
                          >
                            <button
                              class="message-button"
                              onClick={() => props.select(event)}
                              aria-expanded={props.selected === event}
                            >
                              <span>{message}</span>
                              <Show when={props.annotations.get(event)?.length}>
                                <span class="event-indicators">
                                  <For each={props.annotations.get(event)}>
                                    {(indicator) => (
                                      <span class="indicator-label">
                                        <Icon name="bell" size={11} />
                                        {indicator.label}
                                      </span>
                                    )}
                                  </For>
                                </span>
                              </Show>
                            </button>
                          </Show>
                          <button
                            class="icon-button notebook-trigger"
                            aria-label={`Save event ${id} to notebook`}
                            aria-haspopup="dialog"
                            title={
                              saved()?.event === event
                                ? `Saved to ${saved()!.name}`
                                : "Save to Notebook"
                            }
                            classList={{ saved: saved()?.event === event }}
                            onClick={(click) =>
                              setNotebook({
                                event,
                                anchor: click.currentTarget,
                              })
                            }
                          >
                            <Icon
                              name={
                                saved()?.event === event ? "check" : "notebook"
                              }
                              size={16}
                            />
                          </button>
                        </div>
                        <Show when={saved()?.event === event}>
                          <small class="notebook-saved" role="status">
                            Saved to {saved()!.name}
                          </small>
                        </Show>
                      </td>
                      <td class="duration-cell">
                        {duration(valueAt(event, "duration"))}
                      </td>
                    </tr>
                    <tr
                      class="field-row"
                      classList={{
                        selected: props.selected === event,
                        linked: props.reveal?.event === event,
                      }}
                    >
                      <td colSpan={9}>
                        <div class="event-fields" aria-label="Event Fields">
                          <Show when={event.stateLeaf!==undefined}>
                            <span class="field-tag fixed" title="Two-byte State Trie Leaf">
                              <span class="field-name">@leaf</span>
                              <span class="field-value">{`0x${event.stateLeaf!.toString(16).padStart(4,'0').toUpperCase()}`}</span>
                            </span>
                          </Show>
                          <For each={otherFields}>
                            {(item) => (
                              <FieldTag
                                field={item}
                                disabled={props.indicatorsLoading}
                                onIndicator={props.onIndicator}
                                onHistory={props.onObjectHistory&&history()?.field===item.path.join('.')?()=>props.onObjectHistory!(history()!):undefined}
                              />
                            )}
                          </For>
                        </div>
                      </td>
                    </tr>
                    <Show when={props.selected === event}>
                      <tr class="detail-row">
                        <td colSpan={9}>
                          <div class="event-detail">
                            <div class="event-detail-heading">
                              <Icon name="code" size={15} />
                              <span>Event Details</span>
                              <small>{id}</small>
                            </div>
                            <pre>{jsonText(expandEvent(event), 2)}</pre>
                          </div>
                        </td>
                      </tr>
                    </Show>
                  </tbody>
                );
              }}
            </For>
          <tbody aria-hidden="true"><tr class="event-spacer"><td colSpan={9} style={{height:`${windowRange().after}px`}} /></tr></tbody>
        </table>
        <ScrollEnd root={() => table} enabled={!!more() && !props.loading && props.active !== false}
          version={visible().length} onNearEnd={loadMore} />
      </div>
      <Show when={notebook()} keyed>
        {(selection) => (
          <NotebookPicker
            event={selection.event}
            anchor={selection.anchor}
            onClose={() => setNotebook(undefined)}
            onSaved={(name) => {
              setSaved({ event: selection.event, name });
              setNotebook(undefined);
            }}
          />
        )}
      </Show>
    </>
  );
}
