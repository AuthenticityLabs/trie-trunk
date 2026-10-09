import { rawValue, type LogEvent } from "./dictionary.ts";

type Dimension = string | number | boolean | undefined;

class Heap {
  private values: number[] = [];
  private direction: 1 | -1;
  constructor(direction: 1 | -1) {
    this.direction = direction;
  }

  get size() {
    return this.values.length;
  }
  get first() {
    return this.values[0];
  }

  push(value: number) {
    let index = this.values.length;
    this.values.push(value);
    while (index > 0) {
      const parent = Math.floor((index - 1) / 2);
      if (this.direction * (value - this.values[parent]) >= 0) break;
      this.values[index] = this.values[parent];
      index = parent;
    }
    this.values[index] = value;
  }

  pop(): number {
    const first = this.values[0];
    const last = this.values.pop()!;
    if (!this.values.length) return first;
    let index = 0;
    while (index * 2 + 1 < this.values.length) {
      let child = index * 2 + 1;
      if (
        child + 1 < this.values.length &&
        this.direction * (this.values[child + 1] - this.values[child]) < 0
      )
        child++;
      if (this.direction * (last - this.values[child]) <= 0) break;
      this.values[index] = this.values[child];
      index = child;
    }
    this.values[index] = last;
    return first;
  }
}

class Latency {
  private lower = new Heap(-1);
  private upper = new Heap(1);

  get count() {
    return this.lower.size + this.upper.size;
  }
  get p95() {
    return this.lower.first;
  }

  add(value: number) {
    if (!this.lower.size || value <= this.lower.first) this.lower.push(value);
    else this.upper.push(value);
    const target = this.count - Math.floor(this.count / 20);
    if (this.lower.size > target) this.upper.push(this.lower.pop());
    else if (this.lower.size < target) this.lower.push(this.upper.pop());
  }
}

type Group = {
  service: string;
  resource: string;
  operation: string;
  errors: number;
  latency: Latency;
};

export type OperationRank = {
  service: string;
  resource: string;
  operation: string;
  value: number;
  samples: number;
};

function dimension(event: LogEvent, name: string): Dimension {
  const field = event.dictionary.fields.names.get(name);
  const value = rawValue(event, field);
  if (typeof value === "number" && field?.enums.has(value))
    return field.enums.get(value);
  if (
    typeof value === "string" ||
    typeof value === "boolean" ||
    (typeof value === "number" && Number.isFinite(value))
  )
    return value;
  return undefined;
}

function label(value: Dimension): string {
  return value === undefined
    ? "Unspecified"
    : value === ""
      ? "(empty)"
      : String(value);
}

function insert(rows: Group[], group: Group, value: (group: Group) => number) {
  const rank = value(group);
  let i = 0;
  while (i < rows.length && value(rows[i]) >= rank) i++;
  if (i < 10) {
    rows.splice(i, 0, group);
    if (rows.length > 10) rows.pop();
  }
}

export class Operations {
  private services = new Map<
    Dimension,
    Map<Dimension, Map<Dimension, Group>>
  >();
  private groups: Group[] = [];
  private cached?: { errors: OperationRank[]; latency: OperationRank[] };

  add(event: LogEvent, error: boolean, duration: unknown) {
    const measured =
      typeof duration === "number" &&
      Number.isFinite(duration) &&
      duration >= 0;
    if (!error && !measured) return;
    const service = dimension(event, "service");
    const resource = dimension(event, "resource");
    const operation = dimension(event, "operation");
    let resources = this.services.get(service);
    if (!resources) this.services.set(service, (resources = new Map()));
    let operations = resources.get(resource);
    if (!operations) resources.set(resource, (operations = new Map()));
    let group = operations.get(operation);
    if (!group) {
      group = {
        service: label(service),
        resource: label(resource),
        operation: label(operation),
        errors: 0,
        latency: new Latency(),
      };
      operations.set(operation, group);
      this.groups.push(group);
    }
    if (error) group.errors++;
    if (measured) group.latency.add(duration);
    this.cached = undefined;
  }

  top() {
    if (this.cached) return this.cached;
    const errors: Group[] = [];
    const latency: Group[] = [];
    for (const group of this.groups) {
      if (group.errors) insert(errors, group, (group) => group.errors);
      if (group.latency.count)
        insert(latency, group, (group) => group.latency.p95);
    }
    const rows = (groups: Group[], latency: boolean): OperationRank[] =>
      groups.map((group) => ({
        service: group.service,
        resource: group.resource,
        operation: group.operation,
        value: latency ? group.latency.p95 : group.errors,
        samples: latency ? group.latency.count : group.errors,
      }));
    this.cached = { errors: rows(errors, false), latency: rows(latency, true) };
    return this.cached;
  }
}
