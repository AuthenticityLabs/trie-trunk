import { createMemo } from "solid-js";
import {
  VisXYContainer, VisStackedBar, VisFreeBrush, VisCrosshair, VisTooltip,
} from "@unovis/solid";
import { brushWindow } from "../services/charts";

export const chartColors = {
  volume: "var(--chart-green, #a9cd8b)",
  error: "var(--chart-error, #d97668)",
  latency: "var(--chart-blue, #829abe)",
  track: "var(--panel-subtle, #f0f2ec)",
};

export function chartTime(value: number) {
  return Number.isFinite(value)
    ? new Date(value).toISOString().replace("T", " ").slice(5, 19)
    : "—";
}

// Tooltip text is assembled only on hover; data labels never become HTML.
export function chartTooltip(title: string, lines: string[]) {
  const node = document.createElement("div");
  const heading = document.createElement("strong");
  heading.textContent = title;
  node.append(heading);
  for (const line of lines) {
    const row = document.createElement("div");
    row.textContent = line;
    node.append(row);
  }
  return node;
}

export function TimeBrush(props: {
  first: number;
  last: number;
  extent: number;
  onWindow: (from: number, to: number) => void;
}) {
  return <VisFreeBrush
    autoHide
    onBrushEnd={(selection, _event, userDriven) => {
      if (!userDriven) return;
      const window = brushWindow(selection, props.first, props.last, props.extent);
      if (window) props.onWindow(...window);
    }}
  />;
}

export function Histogram(props: {
  counts: number[];
  errors: number[];
  first: number;
  last: number;
  count: number;
  onWindow: (from: number, to: number) => void;
}) {
  // These are bucket indices, not expanded event objects.
  const data = createMemo(() => props.counts.map((_, i) => i));
  const y = [
    (i: number) => props.errors[i],
    (i: number) => props.counts[i] - props.errors[i],
  ];
  return <VisXYContainer
    class="snoot-chart histogram-chart"
    data={data()}
    height={148}
    duration={0}
    xDomain={[0, props.counts.length]}
    yDomain={[0, Math.max(1, ...props.counts)]}
    bleed={{ top: 0, bottom: 0, left: 0, right: 0 }}
    ariaLabel={`${props.count.toLocaleString()} matched events. Drag a time range to search it. Calendar controls also select the time window.`}
  >
    <VisStackedBar
      x={(i: number) => i + 0.5}
      y={y}
      color={[chartColors.error, chartColors.volume]}
      barPadding={0.08}
      roundedCorners={3}
    />
    <VisCrosshair<number>
      x={i => i + 0.5}
      y={i => props.counts[i]}
      color={chartColors.volume}
      hideWhenFarFromPointer={false}
      template={i => chartTooltip(
        `${chartTime(props.first + (props.last - props.first) * i / props.counts.length)} UTC`,
        [`${props.counts[i].toLocaleString()} events`,
          `${props.errors[i].toLocaleString()} errors · ${props.counts[i] ? (100 * props.errors[i] / props.counts[i]).toFixed(1) : "0"}%`],
      )}
    />
    <VisTooltip className="snoot-chart-tooltip" />
    <TimeBrush first={props.first} last={props.last} extent={props.counts.length} onWindow={props.onWindow} />
  </VisXYContainer>;
}

/** A compact Unovis bar shared by breakdowns and operation rankings. */
export function ChartMeter(props: { value: number; maximum: number; color?: string }) {
  const data = createMemo(() => [Math.min(Math.max(0, props.value), Math.max(1, props.maximum))]);
  return <div class="chart-meter" aria-hidden="true">
    <VisXYContainer
      data={data()}
      height={4}
      duration={0}
      xDomain={[0, Math.max(1, props.maximum)]}
      yDomain={[0, 1]}
      bleed={{ top: 0, bottom: 0, left: 0, right: 0 }}
    >
      <VisStackedBar<number>
        orientation="horizontal"
        x={() => 0.5}
        y={value => value}
        color={props.color ?? chartColors.volume}
        barWidth={4}
        roundedCorners={2}
      />
    </VisXYContainer>
  </div>;
}
