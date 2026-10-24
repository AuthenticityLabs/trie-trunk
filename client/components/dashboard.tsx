import { createMemo, Show } from "solid-js";
import { VisXYContainer, VisLine, VisAxis, VisCrosshair, VisTooltip, VisFreeBrush, VisScatter } from "@unovis/solid";
import { CurveType } from "@unovis/ts";
import { ChartProjection, type ChartColumns } from "../services/charts";
import type { LogEvent } from "../services/dictionary";
import { chartColors, chartTime, chartTooltip } from "./charts";

export default function DashboardMetrics(props: {
  records: LogEvent[];
  revision: number;
  onWindow: (from: number, to: number) => void;
}) {
  const projection = new ChartProjection();
  const columns = createMemo(() => {
    const records = props.records;
    const first = records[0]?.millis ?? 0;
    return projection.query(records, props.revision, {
      from: first, to: (records.at(-1)?.millis ?? first) + 1, points: 300,
    });
  });
  return <div class="dashboard-metrics">
    <MetricChart title="Error Rate" columns={columns()} metric="errorRate" onWindow={props.onWindow} />
    <MetricChart title="Mean Duration" columns={columns()} metric="latency" onWindow={props.onWindow} />
  </div>;
}

function MetricChart(props: {
  title: string;
  columns: ChartColumns;
  metric: "errorRate" | "latency";
  onWindow: (from: number, to: number) => void;
}) {
  const values = () => props.columns[props.metric];
  const data = createMemo(() => props.columns.time.map((_, i) => i));
  const maximum = () => Math.max(1, ...values().filter(value => value !== null));
  const color = () => props.metric === "errorRate" ? chartColors.error : chartColors.latency;
  const unit = () => props.metric === "errorRate" ? "%" : "ms";
  const x = (i: number) => props.columns.time[i] +
    Math.min(props.columns.bucketMs, props.columns.to - props.columns.time[i]) / 2;
  return <section class="card metric-card">
    <div class="card-heading">
      <h2>{props.title}</h2>
      <span class="subtle">{unit()}</span>
    </div>
    <Show when={values().some(value => value !== null)} fallback={
      <div class="metric-empty quiet-empty">
        {props.columns.count ? "No measured durations in matched events." : "Collect events to see this chart."}
      </div>
    }>
      <VisXYContainer
        class="snoot-chart metric-chart"
        data={data()}
        height={210}
        duration={0}
        xDomain={[props.columns.from, props.columns.to]}
        yDomain={[0, maximum()]}
        margin={{ top: 12, bottom: 0, left: 0, right: 8 }}
        ariaLabel={`${props.title} across ${props.columns.count.toLocaleString()} matched events. Drag to search a time range.`}
      >
        <VisLine<number>
          x={x}
          y={i => values()[i] ?? NaN}
          color={color()}
          curveType={CurveType.Linear}
          lineWidth={2}
        />
        <VisScatter<number> x={x} y={i => values()[i] ?? NaN} color={color()} size={3} />
        <VisAxis type="y" numTicks={4} tickLine={false} domainLine={false}
          tickFormat={value => Number(value).toLocaleString(undefined, { maximumFractionDigits: 1 })} />
        <VisCrosshair<number>
          x={x}
          y={i => values()[i] ?? NaN}
          color={color()}
          hideWhenFarFromPointer={false}
          template={i => chartTooltip(`${chartTime(props.columns.time[i])} UTC`, [
            values()[i] === null ? "No samples" : `${values()[i]!.toLocaleString(undefined, { maximumFractionDigits: 2 })} ${unit()}`,
            `${props.columns.events[i].toLocaleString()} events`,
          ])}
        />
        <VisTooltip className="snoot-chart-tooltip" />
        <VisTimeBrush columns={props.columns} onWindow={props.onWindow} />
      </VisXYContainer>
      <div class="chart-axis">
        <span>{chartTime(props.columns.from)}</span>
        <span>{chartTime(props.columns.to - 1)} UTC</span>
      </div>
    </Show>
  </section>;
}

// Metric charts use absolute time coordinates, unlike the histogram's buckets.
function VisTimeBrush(props: { columns: ChartColumns; onWindow: (from: number, to: number) => void }) {
  return <VisFreeBrush autoHide onBrushEnd={(selection, _event, userDriven) => {
    if (!userDriven || !selection || typeof selection[0] !== "number" || typeof selection[1] !== "number") return;
    const from = Math.max(props.columns.from, Math.ceil(Math.min(selection[0], selection[1])));
    const to = Math.min(props.columns.to, Math.ceil(Math.max(selection[0], selection[1])));
    if (from < to) props.onWindow(from, to);
  }} />;
}
