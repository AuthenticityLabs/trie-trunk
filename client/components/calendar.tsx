import {zonedTimestamp} from '../services/time-zone';
import {
  DatePicker,
  parseDate,
  type DateValue,
} from "@ark-ui/solid/date-picker";
import { Index, For, Show, createSignal, onCleanup, onMount } from "solid-js";
import { dateWindow, timeWindow, dayCoverage } from "../services/calendar";
import type { Interval } from "../services/coverage";
import type { TimeWindow } from "../services/search";
import { Icon } from "./icon";

export default function Calendar(props: {
  scope: "Collect" | "Search";
  timeZone?:string;
  from: string;
  to: string;
  coverage?: readonly Interval[];
  onChange: (window: TimeWindow, close: boolean, cancel: () => void) => void;
  onExpand?: (window: TimeWindow, cancel: () => void) => void;
  onClose: () => void;
}) {
  const initial = dateWindow(props.from, props.to,Date.now(),props.timeZone);
  const [dates, setDates] = createSignal<DateValue[]>(
    initial.dates.map((date) => parseDate(date)),
  );
  const [startTime, setStartTime] = createSignal(initial.startTime);
  const [endTime, setEndTime] = createSignal(initial.endTime);
  const narrow = matchMedia("(max-width: 680px)");
  const [months, setMonths] = createSignal(narrow.matches ? 1 : 2);
  const [error, setError] = createSignal("");
  const today = parseDate(zonedTimestamp(Date.now(),props.timeZone).slice(0,10));
  let dialog!: HTMLDialogElement;

  onMount(() => {
    const update = () => setMonths(narrow.matches ? 1 : 2);
    update();
    narrow.addEventListener("change", update);
    dialog.showModal();
    onCleanup(() => {
      narrow.removeEventListener("change", update);
      dialog.close();
    });
  });

  function select(value: DateValue[]) {
    const previous = { dates: dates(), start: startTime(), end: endTime() };
    const cancel = () => {
      setDates(previous.dates);
      setStartTime(previous.start);
      setEndTime(previous.end);
    };
    setDates(value);
    setStartTime("00:00:00.000");
    setEndTime("23:59:59.999");
    setError("");
    try {
      const days = value.map((date) => date.toString());
      if (days.length === 1)
        props.onExpand?.(
          timeWindow([days[0], days[0]], startTime(), endTime(),Date.now(),props.timeZone),
          cancel,
        );
      else if (days.length === 2)
        props.onChange(timeWindow(days, startTime(), endTime(),Date.now(),props.timeZone), true, cancel);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }

  function changeTime(side: "start" | "end", value: string) {
    const previous = side === "start" ? startTime() : endTime();
    const setTime = side === "start" ? setStartTime : setEndTime;
    setTime(value);
    try {
      const window = timeWindow(
        dates().map((date) => date.toString()),
        startTime(),
        endTime(),Date.now(),props.timeZone,
      );
      setError("");
      props.onChange(window, false, () => setTime(previous));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }

  return (
    <dialog
      ref={dialog}
      class="modal calendar-modal"
      aria-label={`${props.scope} Time Window`}
      onCancel={(event) => {
        event.preventDefault();
        props.onClose();
      }}
      onClick={(event) => {
        if (event.target === dialog) props.onClose();
      }}
    >
      <div class="modal-body">
        <div class="modal-heading">
          <Show when={props.coverage}>
            <div class="calendar-legend">
              <span>
                <i class="collected" />
                Collection
              </span>
              <span>
                <i class="partial" />
                Partially Collected
              </span>
              <span>
                <i class="selected" />
                Search Window
              </span>
            </div>
          </Show>
          <button
            class="icon-button"
            type="button"
            aria-label="Close Calendar"
            onClick={props.onClose}
          >
            <Icon name="close" size={18} />
          </button>
        </div>
        <DatePicker.Root
          inline
          selectionMode="range"
          value={dates()}
          numOfMonths={months()}
          defaultFocusedValue={dates()[dates().length - 1].subtract({
            months: months() - 1,
          })}
          min={parseDate("1970-01-01")}
          max={today}
          locale="en-CA"
          timeZone={props.timeZone??'UTC'}
          fixedWeeks
          outsideDaySelectable={false}
          format={(date) => date.toString()}
          parse={(value) => {
            try {
              return parseDate(value);
            } catch {
              return undefined;
            }
          }}
          onValueChange={({ value }) => select(value)}
        >
          <DatePicker.Control class="calendar-inputs">
            <div>
              <span class="calendar-input-label">From · {props.timeZone??'UTC'}</span>
              <DatePicker.Input index={0} aria-label="Calendar from date" />
              <input
                type="time"
                step="0.001"
                aria-label={`Calendar From Time ${props.timeZone??'UTC'}`}
                value={startTime()}
                onChange={(event) =>
                  changeTime("start", event.currentTarget.value)
                }
              />
            </div>
            <div>
              <span class="calendar-input-label">Through · {props.timeZone??'UTC'}</span>
              <DatePicker.Input index={1} aria-label="Calendar through date" />
              <input
                type="time"
                step="0.001"
                aria-label={`Calendar Through Time ${props.timeZone??'UTC'}`}
                value={endTime()}
                onChange={(event) =>
                  changeTime("end", event.currentTarget.value)
                }
              />
            </div>
          </DatePicker.Control>
          <DatePicker.Content>
            <DatePicker.View view="day">
              <DatePicker.ViewControl class="calendar-navigation">
                <DatePicker.PrevTrigger
                  class="icon-button"
                  aria-label="Previous month"
                >
                  <Icon name="chevron" class="calendar-previous" />
                </DatePicker.PrevTrigger>
                <div>
                  <DatePicker.MonthSelect aria-label="Calendar month" />
                  <DatePicker.YearSelect aria-label="Calendar year" />
                </div>
                <DatePicker.NextTrigger
                  class="icon-button"
                  aria-label="Next month"
                >
                  <Icon name="chevron" />
                </DatePicker.NextTrigger>
              </DatePicker.ViewControl>
              <DatePicker.Context>
                {(api) => {
                  onMount(() =>
                    api().setFocusedValue(dates()[dates().length - 1]),
                  );
                  return (
                    <div class="calendar-months">
                      <For each={Array.from({ length: months() }, (_, i) => i)}>
                        {(offset) => {
                          const month = () =>
                            api().getOffset({ months: offset });
                          return (
                            <section class="calendar-month">
                              <h3>
                                {api().format(month().visibleRange.start, {
                                  month: "long",
                                  year: "numeric",
                                  timeZone: props.timeZone??'UTC',
                                })}
                              </h3>
                              <DatePicker.Table id={`month-${offset}`}>
                                <DatePicker.TableHead>
                                  <DatePicker.TableRow>
                                    <For each={api().weekDays}>
                                      {(day) => (
                                        <DatePicker.TableHeader>
                                          {day.narrow}
                                        </DatePicker.TableHeader>
                                      )}
                                    </For>
                                  </DatePicker.TableRow>
                                </DatePicker.TableHead>
                                <DatePicker.TableBody>
                                  <Index each={month().weeks}>
                                    {(week) => (
                                      <DatePicker.TableRow>
                                        <Index each={week()}>
                                          {(day) => {
                                            const collected = () =>
                                              dayCoverage(
                                                day().toString(),
                                                props.coverage ?? [],props.timeZone,
                                              );
                                            return (
                                              <DatePicker.TableCell
                                                value={day()}
                                                visibleRange={
                                                  month().visibleRange
                                                }
                                              >
                                                <DatePicker.TableCellTrigger
                                                  data-coverage={collected()}
                                                  title={
                                                    props.coverage
                                                      ? collected() === "full"
                                                        ? "Collected"
                                                        : collected() ===
                                                            "partial"
                                                          ? "Partly collected"
                                                          : "Not collected"
                                                      : undefined
                                                  }
                                                >
                                                  {day().day}
                                                </DatePicker.TableCellTrigger>
                                              </DatePicker.TableCell>
                                            );
                                          }}
                                        </Index>
                                      </DatePicker.TableRow>
                                    )}
                                  </Index>
                                </DatePicker.TableBody>
                              </DatePicker.Table>
                            </section>
                          );
                        }}
                      </For>
                    </div>
                  );
                }}
              </DatePicker.Context>
            </DatePicker.View>
          </DatePicker.Content>
        </DatePicker.Root>
        <Show when={error()}>
          <p class="calendar-error" role="alert">
            {error()}
          </p>
        </Show>
      </div>
    </dialog>
  );
}
