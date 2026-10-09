import { Results } from "./results.ts";
import { rawValue, decodeValue, type LogEvent } from "./dictionary.ts";
import { parameterNames, type Choices, type Predicate } from "./search.ts";
import type { Matcher } from "./indicators.ts";

const yieldToBrowser = () =>
  new Promise<void>((resolve) => {
    const channel = new MessageChannel();
    channel.port1.onmessage = () => {
      channel.port1.close();
      channel.port2.close();
      resolve();
    };
    channel.port2.postMessage(null);
  });

export class WorkingSet {
  readonly collected = new Results();
  visible = this.collected;
  private predicate?: Predicate;
  private matchers: Matcher[] = [];
  private revision = 0;
  private pending?: { results: Results; predicate: Predicate };

  private values = new Map(
    parameterNames.map((name) => [name, new Set<string>()]),
  );
  private options: Choices = {};

  constructor(predicate?: Predicate) {
    this.predicate = predicate;
    if (predicate) this.visible = new Results();
  }

  choices(): Choices {
    return this.options;
  }

  setMatchers(matchers: Matcher[]) {
    this.matchers = matchers;
    this.collected.setMatchers(matchers);
    if (this.visible !== this.collected) this.visible.setMatchers(matchers);
    this.pending?.results.setMatchers(matchers);
  }

  append(events: readonly LogEvent[]): number {
    const accepted: LogEvent[] = [];
    const added = this.collected.append(events, (event) =>
      accepted.push(event),
    );
    if (added) {
      let changed = false;
      for (const event of accepted) {
        for (const name of parameterNames) {
          const field = event.dictionary.fields.names.get(name);
          const raw = rawValue(event, field);
          if (raw != null && typeof raw !== "object") {
            const label = decodeValue(raw, field);
            if (typeof label !== "string") continue;
            const values = this.values.get(name)!;
            if (!values.has(label)) {
              values.add(label);
              changed = true;
            }
          }
        }
      }
      if (changed)
        this.options = Object.fromEntries(
          [...this.values].map(([name, values]) => [name, [...values].sort()]),
        );
      if (this.predicate) this.visible.append(accepted.filter(this.predicate));
      if (this.pending)
        this.pending.results.append(accepted.filter(this.pending.predicate));
    }
    return added;
  }

  cancel() {
    this.revision++;
    this.pending = undefined;
  }

  async search(
    predicate?: Predicate,
    pause = yieldToBrowser,
  ): Promise<boolean> {
    this.cancel();
    const revision = this.revision;
    if (!predicate) {
      this.predicate = undefined;
      this.visible = this.collected;
      return true;
    }
    const source = this.collected.events.slice();
    const results = new Results();
    results.setMatchers(this.matchers);
    this.pending = { results, predicate };
    for (let offset = 0; offset < source.length; offset += 2048) {
      results.append(source.slice(offset, offset + 2048).filter(predicate));
      await pause();
      if (revision !== this.revision) return false;
    }
    this.visible = results;
    this.predicate = predicate;
    this.pending = undefined;
    return true;
  }
}
