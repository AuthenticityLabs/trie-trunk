import { For, Show, createEffect, createSignal, on } from "solid-js";
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
import { pageSizes } from "../services/preferences";
import {
  eventFields,
  type Annotations,
  type EventField,
} from "../services/indicators";

const clock = new Intl.DateTimeFormat("en-GB", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  fractionalSecondDigits: 3,
  timeZone: "UTC",
});

export function eventTime(event: LogEvent): string {
  return clock.format(event.millis);
}

function FieldTag(props: {
  field: EventField;
  disabled: boolean;
  onIndicator: (field: EventField) => void;
}) {
  return (
    <Dynamic
      component={props.field.fixed ? "span" : "button"}
      class="field-tag"
      classList={{ fixed: props.field.fixed }}
      disabled={props.field.fixed ? undefined : props.disabled}
      aria-haspopup={props.field.fixed ? undefined : "dialog"}
      aria-label={
        props.field.fixed
          ? undefined
          : `Add indicator for ${props.field.path.join(".")}`
      }
      title={`${props.field.path.join(".")}: ${props.field.text}${props.field.fixed ? "" : "\nAdd indicator"}`}
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
  );
}

export function Events(props: {
  revision: number;
  events: LogEvent[];
  selected: LogEvent | undefined;
  select: (event: LogEvent) => void;
  annotations: Annotations;
  onIndicator: (field: EventField) => void;
  indicatorsLoading: boolean;
  reveal: { event: LogEvent } | undefined;
  pageSize: number;
  onPageSize: (value: number) => Promise<void>;
  preferencesBusy: boolean;
}) {
  const [page, setPage] = createSignal(0);
  const [notebook, setNotebook] = createSignal<{
    event: LogEvent;
    anchor: HTMLElement;
  }>();
  const [saved, setSaved] = createSignal<{ event: LogEvent; name: string }>();
  let table: HTMLDivElement | undefined;
  const pages = () =>
    Math.max(1, Math.ceil(props.events.length / props.pageSize));
  const current = () => Math.min(page(), pages() - 1);
  const visible = () =>
    props.events.slice(
      current() * props.pageSize,
      (current() + 1) * props.pageSize,
    );

  createEffect(
    on(
      () => props.reveal,
      (request) => {
        if (!request) return;
        const index = props.events.indexOf(request.event);
        if (index < 0) return;
        setPage(Math.floor(index / props.pageSize));
        queueMicrotask(() => {
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
        setPage(0);
        setNotebook(undefined);
      },
    ),
  );

  async function changeSize(
    event: Event & { currentTarget: HTMLSelectElement },
  ) {
    const value = Number(event.currentTarget.value);
    const first = current() * props.pageSize;
    event.currentTarget.value = String(props.pageSize);
    try {
      await props.onPageSize(value);
      setPage(Math.floor(first / value));
    } catch {
      return;
    }
  }
  return (
    <>
      <div class="table-scroll" ref={table}>
        <table class="event-table">
          <thead>
            <tr>
              <th class="expand-cell">
                <span class="sr-only">Expand</span>
              </th>
              <th>
                Time <span>UTC</span>
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
          <tbody>
            <For each={visible()}>
              {(event, index) => {
                const id = eventId(event);
                const message = field(event, "message");
                const fields = eventFields(event);
                const otherFields = fields.filter(
                  (item) =>
                    !["id", "resource", "operation"].includes(item.path[0]),
                );
                const severity = level(event);
                return (
                  <>
                    <tr
                      class="event-summary"
                      id={`event-${id}`}
                      data-event-row={current() * props.pageSize + index()}
                      tabIndex={-1}
                      classList={{
                        selected: props.selected === event,
                        linked: props.reveal?.event === event,
                      }}
                    >
                      <td class="expand-cell">
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
                      </td>
                      <td class="time-cell" title={field(event, "time")}>
                        {eventTime(event)}
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
                                : "Save to notebook"
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
                        <div class="event-fields" aria-label="Event fields">
                          <For each={otherFields}>
                            {(item) => (
                              <FieldTag
                                field={item}
                                disabled={props.indicatorsLoading}
                                onIndicator={props.onIndicator}
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
                              <span>Event details</span>
                              <small>{id}</small>
                            </div>
                            <pre>{jsonText(expandEvent(event), 2)}</pre>
                          </div>
                        </td>
                      </tr>
                    </Show>
                  </>
                );
              }}
            </For>
          </tbody>
        </table>
      </div>
      <div class="pagination">
        <span>
          {current() * props.pageSize + 1}–
          {Math.min((current() + 1) * props.pageSize, props.events.length)} of{" "}
          {props.events.length.toLocaleString()}
        </span>
        <label class="page-size">
          Events per page
          <select
            aria-label="Events per page"
            value={props.pageSize}
            disabled={props.preferencesBusy}
            onChange={(event) => void changeSize(event)}
          >
            <For each={pageSizes}>
              {(size) => <option value={size}>{size}</option>}
            </For>
          </select>
        </label>
        <div class="page-buttons">
          <button
            class="text-button"
            disabled={current() === 0}
            onClick={() => setPage(current() - 1)}
          >
            Previous
          </button>
          <button
            class="text-button"
            disabled={current() === pages() - 1}
            onClick={() => setPage(current() + 1)}
          >
            Next
            <Icon name="chevron" size={12} />
          </button>
        </div>
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
