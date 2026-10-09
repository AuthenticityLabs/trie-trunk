export type Interval = { from: number; to: number };

export class Coverage {
  private ranges: Interval[] = [];

  intervals(): readonly Interval[] {
    return this.ranges;
  }

  add(range: Interval) {
    if (range.from >= range.to) return;
    const merged: Interval[] = [];
    for (const next of [...this.ranges, { ...range }].sort(
      (a, b) => a.from - b.from,
    )) {
      const last = merged.at(-1);
      if (last && next.from <= last.to) last.to = Math.max(last.to, next.to);
      else merged.push(next);
    }
    this.ranges = merged;
  }

  missing(range: Interval): Interval[] {
    const gaps: Interval[] = [];
    let from = range.from;
    for (const covered of this.ranges) {
      if (covered.to <= from) continue;
      if (covered.from >= range.to) break;
      if (covered.from > from) gaps.push({ from, to: covered.from });
      from = Math.max(from, covered.to);
      if (from >= range.to) return gaps;
    }
    if (from < range.to) gaps.push({ from, to: range.to });
    return gaps;
  }
}

export function describeInterval(range: Interval): string {
  const display = (millis: number) =>
    new Date(millis)
      .toISOString()
      .replace("T", " ")
      .replace(/\.000Z$|Z$/g, "");
  return `${display(range.from)} → ${display(range.to)} UTC`;
}
