/** Measured row heights; prefix sums and viewport lookup stay O(log n). */
export class RowWindow {
  private heights: number[] = [];
  private sums = new Float64Array(1);
  private estimate: number;
  constructor(estimate = 120) { this.estimate = estimate; }
  get length() { return this.heights.length; }
  resize(length: number, reset = false) {
    if (!reset && length === this.length) return;
    this.heights = Array.from({length}, (_, i) => reset ? this.estimate : this.heights[i] ?? this.estimate);
    this.sums = new Float64Array(length + 1);
    for (let i = 1; i <= length; i++) {
      this.sums[i] += this.heights[i - 1];
      const parent = i + (i & -i);
      if (parent <= length) this.sums[parent] += this.sums[i];
    }
  }
  offset(index: number) {
    let sum = 0;
    for (let i = Math.min(index, this.length); i > 0; i -= i & -i) sum += this.sums[i];
    return sum;
  }
  measure(index: number, height: number) {
    if (index < 0 || index >= this.length || !Number.isFinite(height) || height <= 0) return 0;
    const delta = height - this.heights[index];
    this.heights[index] = height;
    for (let i = index + 1; i <= this.length; i += i & -i) this.sums[i] += delta;
    return delta;
  }
  index(offset: number) {
    let index = 0, sum = 0;
    let bit = 1;
    while (bit * 2 <= this.length) bit *= 2;
    for (; bit; bit >>= 1) {
      const next = index + bit;
      if (next <= this.length && sum + this.sums[next] <= offset) {
        index = next; sum += this.sums[next];
      }
    }
    return Math.min(index, Math.max(0, this.length - 1));
  }
  range(top: number, height: number, overscan = 300) {
    const start = this.index(Math.max(0, top - overscan));
    const end = Math.min(this.length, this.index(top + height + overscan) + 1);
    return {start, end, before:this.offset(start), after:this.offset(this.length) - this.offset(end)};
  }
}
