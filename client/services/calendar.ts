import {zonedTimestamp,parseZonedTimestamp} from './time-zone.ts';
import type { Interval } from "./coverage.ts";

export function dateWindow(from: string, to: string, now = Date.now(),zone="UTC") {
  const start = from ? Date.parse(`${from}Z`) : now;
  const end = to ? Date.parse(`${to}Z`) - 1 : now;
  const first = zonedTimestamp(Number.isFinite(start)?start:now,zone)+"Z";
  const last = zonedTimestamp(Number.isFinite(end)?end:now,zone)+"Z";
  return {
    dates: [first.slice(0, 10), last.slice(0, 10)],
    startTime: first.slice(11, -1),
    endTime: last.slice(11, -1),
  };
}

export function timeWindow(
  dates: string[],
  from: string,
  through: string,
  now = Date.now(),
  zone="UTC",
) {
  if (dates.length !== 2 || !from || !through)
    throw new Error("Choose both dates and times.");
  const start = parseZonedTimestamp(`${dates[0]}T${from}`,zone);
  const end = Math.min(parseZonedTimestamp(`${dates[1]}T${through}`,zone) + 1, now);
  if (
    !Number.isFinite(start) ||
    !Number.isFinite(end) ||
    start < 0 ||
    start >= end
  )
    throw new Error("Choose a start before the end of the window.");
  return {
    from: new Date(start).toISOString().slice(0, -1),
    to: new Date(end).toISOString().slice(0, -1),
  };
}

export function dayCoverage(day: string, ranges: readonly Interval[],zone="UTC") {
  const from = parseZonedTimestamp(`${day}T00:00:00`,zone),
    next=new Date(Date.parse(day+"T00:00:00Z")+86400000).toISOString().slice(0,10),to=parseZonedTimestamp(`${next}T00:00:00`,zone);
  let covered = 0;
  for (const range of ranges) {
    if (range.from >= to) break;
    covered += Math.max(0, Math.min(to, range.to) - Math.max(from, range.from));
  }
  return covered >= to-from ? "full" : covered > 0 ? "partial" : undefined;
}

export function windowLabel(from?: string, to?: string,zone="UTC") {
  if (!from && !to) return `Choose Dates · ${zone}`;
  const show = (value?: string) =>
    value
      ? zonedTimestamp(Date.parse(value.replace(/Z$/,'')+'Z'),zone)
          .replace("T", " ")
          .replace(/:00(?:\.000)?$/, "")
          .replace(/\.000$/, "")
      : "Now";
  return `${show(from)} → ${show(to)} ${zone}`;
}
