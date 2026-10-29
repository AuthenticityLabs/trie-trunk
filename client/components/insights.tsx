import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  createUniqueId,
  onCleanup,
} from "solid-js";
import { duration, field, type LogEvent } from "../services/events";
import type { Signal } from "../services/indicators";
import { Icon } from "./icon";
import { Histogram, ChartMeter, chartColors } from "./charts";

import type { Results } from "../services/results";
import type { OperationRank } from "../services/operations";

type Summary = ReturnType<Results["summary"]>;
const indicatorOperators = {
  text: "contains",
  equals: "equals",
  regex: "regex",
  lte: "≤",
  gte: "≥",
};

export function Activity(props: {
  summary: Summary;
  count: number;
  onWindow: (from: number, to: number) => void;
  onResetWindow: () => void;
  hasWindow: boolean;
  onRemoveIndicator: (id: string) => Promise<void>;
  onRevealEvent: (event: LogEvent) => void;
}) {
  const [hovered, setHovered] = createSignal<number>();
  const [selected, setSelected] = createSignal<number>();
  const [deleting, setDeleting] = createSignal(false);
  const [deleteError, setDeleteError] = createSignal("");
  const markers = createMemo(() => {
    const groups = new Map<number, Signal[]>();
    for (const signal of props.summary.signals) {
      const group = groups.get(signal.bucket);
      if (group) group.push(signal);
      else groups.set(signal.bucket, [signal]);
    }
    return [...groups].map(([bucket, signals]) => ({
      bucket,
      signals,
      count: props.summary.indicatorBuckets[bucket],
      label: signals.map((signal) => signal.indicator.label).join(", "),
    }));
  });
  const active = () =>
    markers().find((marker) => marker.bucket === (selected() ?? hovered()));
  const maximum = () => Math.max(1, ...props.summary.buckets);
  const position = (bucket: number) =>
    ((bucket + 0.5) / props.summary.buckets.length) * 100;
  const time = (value: number) =>
    Number.isFinite(value)
      ? new Date(value).toISOString().replace("T", " ").slice(5, 19)
      : "—";
  const rate = (errors: number, count: number) =>
    count ? `${((100 * errors) / count).toFixed(1)}%` : "0%";
  let panel: HTMLDivElement | undefined;
  let closeTimer: ReturnType<typeof setTimeout> | undefined;

  function close() {
    clearTimeout(closeTimer);
    setSelected(undefined);
    setHovered(undefined);
    setDeleteError("");
  }

  async function remove(id: string) {
    setDeleting(true);
    setDeleteError("");
    try {
      await props.onRemoveIndicator(id);
    } catch (cause) {
      setDeleteError(
        `Could not delete indicator: ${cause instanceof Error ? cause.message : String(cause)}`,
      );
    } finally {
      setDeleting(false);
    }
  }

  function leave() {
    clearTimeout(closeTimer);
    closeTimer = setTimeout(() => setHovered(undefined), 120);
  }

  createEffect(() => {
    if (!active()) return;
    const dismiss = (event: PointerEvent) => {
      const target = event.target as Element;
      if (!panel?.contains(target) && !target.closest(".signal-bubble"))
        close();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", escape);
    onCleanup(() => {
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", escape);
    });
  });
  onCleanup(() => clearTimeout(closeTimer));

  return (
    <section class="card activity-card">
      <div class="card-heading">
        <h2>
          <Icon name="activity" size={17} /> Event Activity
        </h2>
        <div class="chart-heading-actions">
          <span class="subtle">{rate(props.summary.errors, props.count)} errors</span>
          <Show when={props.hasWindow}>
            <button class="text-button" onClick={props.onResetWindow}>Reset Time</button>
          </Show>
        </div>
      </div>
      <div class="chart-legend">
        <span>
          <i class="volume-swatch" /> Other events
        </span>
        <span>
          <i class="error-swatch" /> Errors
        </span>
        <span>UTC</span>
      </div>
      <div class="timeline-plot">
        <Histogram
          counts={props.summary.buckets}
          errors={props.summary.errorBuckets}
          first={props.summary.first}
          last={props.summary.last}
          count={props.count}
          onWindow={(from, to) => { close(); props.onWindow(from, to); }}
        />
        <For each={markers()}>
          {(marker) => (
            <button
              class="signal-bubble tone-blue"
              classList={{ active: active()?.bucket === marker.bucket }}
              style={{
                left: `${position(marker.bucket)}%`,
                bottom: `calc(var(--plot-height) * ${Math.max(0.04, props.summary.buckets[marker.bucket] / maximum())} + 5px)`,
              }}
              aria-label={`${marker.label} · ${marker.count} event${marker.count === 1 ? "" : "s"}`}
              aria-haspopup="dialog"
              aria-expanded={active()?.bucket === marker.bucket}
              onPointerEnter={() => {
                clearTimeout(closeTimer);
                setHovered(marker.bucket);
              }}
              onPointerLeave={leave}
              onFocus={() => {
                clearTimeout(closeTimer);
                setHovered(marker.bucket);
              }}
              onBlur={leave}
              onClick={() => {
                if (selected() === marker.bucket) close();
                else setSelected(marker.bucket);
              }}
            >
              <Show
                when={marker.count > 1}
                fallback={<Icon name="bell" size={13} />}
              >
                {marker.count}
              </Show>
            </button>
          )}
        </For>
        <Show when={active()} keyed>
          {(marker) => (
            <div
              ref={panel}
              class="signal-detail tone-blue"
              role="dialog"
              aria-label="Timeline Event Details"
              style={{
                left: `clamp(0px, calc(${position(marker.bucket)}% - 165px), max(0px, calc(100% - 330px)))`,
                top: `calc(var(--plot-height) * ${1 - Math.max(0.04, props.summary.buckets[marker.bucket] / maximum())} + 30px)`,
              }}
              onPointerEnter={() => clearTimeout(closeTimer)}
              onPointerLeave={leave}
              onPointerDown={() => setSelected(marker.bucket)}
              onFocusIn={() => {
                clearTimeout(closeTimer);
                setSelected(marker.bucket);
              }}
            >
              <div class="signal-detail-heading">
                <strong>
                  {marker.count} event{marker.count === 1 ? "" : "s"}
                </strong>
                <button
                  class="icon-button"
                  aria-label="Close Indicator Details"
                  onClick={close}
                >
                  <Icon name="close" size={14} />
                </button>
              </div>
              <Show when={deleteError()}>
                <p class="indicator-error" role="alert">
                  {deleteError()}
                </p>
              </Show>
              <For each={marker.signals}>
                {(signal) => (
                  <div class="signal-group">
                    <div class="signal-group-heading">
                      <strong>
                        <Icon name="bell" size={13} /> {signal.indicator.label}
                      </strong>
                      <button
                        class="text-button delete-indicator"
                        aria-label={`Delete indicator ${signal.indicator.label}`}
                        disabled={deleting()}
                        onClick={() => void remove(signal.indicator.id)}
                      >
                        Delete
                      </button>
                    </div>
                    <div class="signal-rule">
                      <code>{signal.indicator.path.join(".")}</code> ·{" "}
                      {indicatorOperators[signal.indicator.mode]}{" "}
                      <code>{signal.indicator.pattern || '""'}</code>
                      {signal.indicator.ignoreCase ? " · ignore case" : ""}
                    </div>
                    <For each={signal.events}>
                      {(event) => (
                        <a
                          class="signal-event-link"
                          href={`#event-${field(event, "id")}`}
                          onClick={(click) => {
                            click.preventDefault();
                            close();
                            props.onRevealEvent(event);
                          }}
                        >
                          <Icon name="arrow" size={12} />
                          <span>
                            {time(event.millis)} UTC · {field(event, "service")}{" "}
                            ·{" "}
                            {field(event, "release") || field(event, "message")}
                          </span>
                        </a>
                      )}
                    </For>
                  </div>
                )}
              </For>
            </div>
          )}
        </Show>
      </div>
      <div class="chart-axis">
        <span>{time(props.summary.first)}</span>
        <span>{time(props.summary.last)}</span>
      </div>
    </section>
  );
}

export function Breakdown(props: {
  title: string;
  values: [string, number][];
  total: number;
  onSelect?: (scope: "Collect" | "Search", value: string) => void;
  levels?: boolean;
}) {
  return (
    <section class="card breakdown-card">
      <div class="card-heading">
        <h2>{props.title}</h2>
        <span class="subtle">{props.values.length}</span>
      </div>
      <Show
        when={props.values.length}
        fallback={
          <p class="quiet-empty">Collect events to see the breakdown.</p>
        }
      >
        <div class="breakdown-list">
          <For each={props.values.slice(0, 6)}>
            {([name, count]) => (
              <div class="breakdown-row">
                <div>
                  <span classList={{ "severity-label": props.levels }}>
                    <Show when={props.levels}>
                      <i class={`severity-dot dot-${name.toLowerCase()}`} />
                    </Show>
                    {name}
                  </span>
                  <strong>
                    {count.toLocaleString()}{" "}
                    <small>
                      {Math.round((count / Math.max(1, props.total)) * 100)}%
                    </small>
                  </strong>
                </div>
                <ChartMeter value={count} maximum={props.total} />
                <Show
                  when={
                    props.onSelect &&
                    name !== "Unspecified" &&
                    name !== "UNKNOWN"
                  }
                >
                  <div class="breakdown-actions">
                    <button
                      class="text-button"
                      aria-label={`Use ${name} in Search`}
                      onClick={() => props.onSelect?.("Search", name)}
                    >
                      Use in Search
                    </button>
                  </div>
                </Show>
              </div>
            )}
          </For>
        </div>
      </Show>
    </section>
  );
}

export function Overview(props: { summary: Summary; count: number }) {
  return (
    <section class="card overview-card">
      <h2>Matched Overview</h2>
      <div class="large-stat">
        {props.count.toLocaleString()}
        <span>events matched</span>
      </div>
      <dl>
        <div>
          <dt>Services</dt>
          <dd>{props.summary.services.length}</dd>
        </div>
        <div>
          <dt>Errors</dt>
          <dd>{props.summary.errors.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Average Duration</dt>
          <dd>{duration(props.summary.average)}</dd>
        </div>
      </dl>
      <div class="insight-note">
        <Icon name="leaf" size={17} />
        <span>
          Summaries reflect the matched subset of the collected working set.
        </span>
      </div>
    </section>
  );
}

export function OperationRanking(props: {
  rows: OperationRank[];
  kind: "errors" | "latency";
  count: number;
}) {
  const [expanded, setExpanded] = createSignal(false);
  const listId = createUniqueId();
  const title = () => (props.kind === "errors" ? "Top Errors" : "Most Latent");
  const maximum = () => Math.max(1, props.rows[0]?.value ?? 0);
  return (
    <section class="card ranking-card" aria-label={title()}>
      <div class="card-heading">
        <h2>{title()}</h2>
        <Icon name={props.kind === "errors" ? "bell" : "clock"} size={14} />
      </div>
      <p class="ranking-caption">Service / Resource / Operation</p>
      <Show
        when={props.rows.length}
        fallback={
          <p class="quiet-empty">
            {!props.count
              ? "Load events to see rankings."
              : props.kind === "errors"
                ? "No errors in matched events."
                : "No measured durations."}
          </p>
        }
      >
        <ol class="ranking-list" id={listId}>
          <For each={expanded() ? props.rows : props.rows.slice(0, 2)}>
            {(row, index) => (
              <li class="ranking-row">
                <span class="ranking-position">{index() + 1}</span>
                <div class="ranking-body">
                  <div class="ranking-service">
                    <strong title={row.service}>{row.service}</strong>
                    <span>
                      {props.kind === "errors"
                        ? row.value.toLocaleString()
                        : duration(row.value)}
                    </span>
                  </div>
                  <div
                    class="ranking-operation"
                    title={`${row.resource} / ${row.operation}`}
                  >
                    <span>{row.resource}</span>
                    <span class="ranking-separator">/</span>
                    <span>{row.operation}</span>
                  </div>
                  <Show when={props.kind === "latency"}>
                    <small class="ranking-samples">
                      {row.statistic === "average" ? "Average" : "P95"} · {row.samples.toLocaleString()}{" "}
                      {row.samples === 1 ? "sample" : "samples"}
                    </small>
                  </Show>
                  <ChartMeter value={row.value} maximum={maximum()}
                    color={props.kind === "errors" ? chartColors.error : chartColors.volume} />
                </div>
              </li>
            )}
          </For>
        </ol>
        <Show when={props.rows.length > 2}>
          <button
            class="text-button ranking-toggle"
            aria-expanded={expanded()}
            aria-controls={listId}
            aria-label={`${expanded() ? "Collapse" : "Expand"} ${title()}`}
            onClick={() => setExpanded(!expanded())}
          >
            {expanded() ? "Show less" : `Show all ${props.rows.length}`}
            <Icon name="down" size={12} />
          </button>
        </Show>
      </Show>
      <p class="ranking-scope">
        Matched events ·{" "}
        {props.kind === "errors" ? "error count" : props.rows.some(row => row.statistic === "average") ? "average duration" : "p95 duration"}
      </p>
    </section>
  );
}
