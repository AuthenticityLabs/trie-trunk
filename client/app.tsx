import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
  lazy,
  untrack,
} from "solid-js";
import { beginCollection, restoreCollection } from "./services/pil/client";
import wordmark from "./assets/snoot-wordmark.png";
import { Coverage, type Interval } from "./services/coverage";
import { ExpansionDialog } from "./components/expansion";
import { Events } from "./components/events";
import { Icon, Mark } from "./components/icon";
import { Navigation, type Page } from "./components/navigation";
import { Settings, type Panel } from "./components/settings";
import {
  Activity,
  Breakdown,
  Overview,
  OperationRanking,
} from "./components/insights";
import { IndicatorDialog } from "./components/indicator";
import {
  defaultPageSize,
  loadPageSize,
  loadTheme,
  savePageSize,
  saveTheme,
  type Theme,
} from "./services/preferences";
import {
  compileIndicator,
  loadIndicators,
  saveIndicator,
  deleteIndicator,
  type EventField,
  type Indicator,
} from "./services/indicators";
import {
  loadDictionaries,
  streamEvents,
  listTaps,
  type LogEvent,
  type Selection,
} from "./services/events";
import { windowLabel } from "./services/calendar";
import { WorkingSet } from "./services/working-set";
import { Criteria } from "./components/criteria";
import {
  collectionWindow,
  compileSearch,
  describeSearch,
  parameterNames,
  where,
  type Choices,
  type Parameters,
  type LocalSelection,
  type Predicate,
  type TimeWindow,
} from "./services/search";
import { expandEvent, jsonText } from "./services/dictionary";

const Calendar = lazy(() => import("./components/calendar"));

type Status =
  | "restoring"
  | "restored"
  | "idle"
  | "connecting"
  | "reading"
  | "following"
  | "ended"
  | "stopped"
  | "error";
export default function App() {
  const [theme, setTheme] = createSignal<Theme>("light");
  const [themeBusy, setThemeBusy] = createSignal(false);
  const [themeError, setThemeError] = createSignal("");
  let themeChanged = false;
  createEffect(() => {
    document.documentElement.dataset.theme = theme();
    document.querySelector('meta[name="theme-color"]')?.setAttribute(
      "content",
      theme() === "dark" ? "#1b211e" : "#f8f7f5",
    );
  });

  async function toggleTheme() {
    if (themeBusy()) return;
    themeChanged = true;
    const next = theme() === "light" ? "dark" : "light";
    setTheme(next);
    setThemeBusy(true);
    setThemeError("");
    try {
      await saveTheme(next);
    } catch {
      setThemeError("Could not save the theme for your next visit.");
    } finally {
      setThemeBusy(false);
    }
  }

  let working = new WorkingSet();
  let coverage = new Coverage();
  let collectionToken = "";
  let collectionId: string | undefined;
  const [calendar, setCalendar] = createSignal<"Collect" | "Search">();
  const [covered, setCovered] = createSignal<readonly Interval[]>([]);
  const [collectionScope, setCollectionScope] = createSignal("");
  const [expansion, setExpansion] = createSignal<{
    ranges: Interval[];
    cancel: () => void;
    apply: boolean;
  }>();
  let activePredicate: Predicate | undefined;
  const [page, setPage] = createSignal<Page>("Search");
  const [drawer, setDrawer] = createSignal(false);
  const [collapsed, setCollapsed] = createSignal(false);
  const [modal, setModal] = createSignal<Panel>();
  const [token, setToken] = createSignal("");
  const [tap, setTap] = createSignal("");
  const [taps, setTaps] = createSignal<string[]>([]);
  const [tapError, setTapError] = createSignal("");
  const [tapsLoading, setTapsLoading] = createSignal(false);
  const [tapRefresh, setTapRefresh] = createSignal(0);
  const [range, setRange] = createSignal("today");
  const [from, setFrom] = createSignal("");
  const [to, setTo] = createSignal("");
  const [parameters, setParameters] = createSignal<Parameters>({});
  const [choices, setChoices] = createSignal<Choices>({});
  const [schemaError, setSchemaError] = createSignal("");
  const [schemaLoading, setSchemaLoading] = createSignal(false);
  const [localParameters, setLocalParameters] = createSignal<Parameters>({});
  const [localChoices, setLocalChoices] = createSignal<Choices>({});
  const [localTap, setLocalTap] = createSignal("");
  const [localFrom, setLocalFrom] = createSignal("");
  const [localTo, setLocalTo] = createSignal("");
  const [activeSearch, setActiveSearch] = createSignal<LocalSelection>();
  const [searchError, setSearchError] = createSignal("");
  const [searching, setSearching] = createSignal(false);
  const [collected, setCollected] = createSignal(0);
  const [availableWindow, setAvailableWindow] = createSignal<TimeWindow>();
  const [viewRevision, setViewRevision] = createSignal(0);
  const [collectionDraft, setCollectionDraft] = createSignal("");
  const collectionParameters = () =>
    JSON.stringify([tap(), range(), from(), to(), parameters()]);
  const localDraft = (): LocalSelection => ({
    parameters: Object.fromEntries(
      parameterNames
        .map((name) => [name, localParameters()[name]])
        .filter(([, value]) => value),
    ),
    tap: localTap(),
    from: localFrom(),
    to: localTo(),
  });
  const searchChanged = () =>
    JSON.stringify(localDraft()) !==
    JSON.stringify(
      activeSearch() ?? {
        parameters: {},
        tap: "",
        from: "",
        to: "",
      },
    );
  const [records, setRecords] = createSignal<LogEvent[]>(
    working.visible.events,
    {
      equals: false,
    },
  );
  const [selected, setSelected] = createSignal<LogEvent>();
  const [revealed, setRevealed] = createSignal<{ event: LogEvent }>();
  const [pageSize, setPageSize] = createSignal(defaultPageSize);
  const [preferencesBusy, setPreferencesBusy] = createSignal(true);
  const [preferencesError, setPreferencesError] = createSignal("");
  const [status, setStatus] = createSignal<Status>("idle");
  const [error, setError] = createSignal("");
  const [copied, setCopied] = createSignal(false);
  const [hasCollected, setHasCollected] = createSignal(false);
  const [activeSelection, setActiveSelection] = createSignal<Selection>();
  const [indicators, setIndicators] = createSignal<Indicator[]>([]);
  const [indicatorField, setIndicatorField] = createSignal<EventField>();
  const [indicatorError, setIndicatorError] = createSignal("");
  const [indicatorsLoading, setIndicatorsLoading] = createSignal(true);
  const matchers = createMemo(() => indicators().map(compileIndicator));
  const [annotations, setAnnotations] = createSignal(
    working.visible.annotations,
    {
      equals: false,
    },
  );
  const [summary, setSummary] = createSignal(working.visible.summary());
  createEffect(() => {
    working.setMatchers(matchers());
    setAnnotations(working.visible.annotations);
    setSummary(working.visible.summary());
  });
  const running = () =>
    ["restoring", "connecting", "reading", "following"].includes(status());
  const statusLabel = () =>
    ({
      idle: "Ready to collect",
      restoring: "Restoring collection…",
      restored: "Local collection",
      connecting: "Connecting…",
      reading: "Receiving events",
      following: "Following live",
      ended: "Stream ended",
      stopped: "Stream stopped",
      error: "Collection needs attention",
    })[status()];
  let controller: AbortController | undefined;
  let generation = 0;
  let frame = 0;
  let queued: LogEvent[] = [];
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  let localCard: HTMLFormElement | undefined;
  let collectionCard: HTMLFormElement | undefined;
  let searchRevision = 0;
  let disposed = false;

  async function loadSavedIndicators() {
    setIndicatorsLoading(true);
    setIndicatorError("");
    try {
      const saved = await loadIndicators();
      if (!disposed) setIndicators(saved);
    } catch (cause) {
      if (!disposed)
        setIndicatorError(
          `Could not load indicators: ${cause instanceof Error ? cause.message : String(cause)}`,
        );
    } finally {
      if (!disposed) setIndicatorsLoading(false);
    }
  }

  async function loadPreferences() {
    setPreferencesBusy(true);
    setPreferencesError("");
    try {
      const size = await loadPageSize();
      if (!disposed) setPageSize(size);
    } catch (cause) {
      if (!disposed)
        setPreferencesError(
          `Could not load preferences: ${cause instanceof Error ? cause.message : String(cause)}`,
        );
    } finally {
      if (!disposed) setPreferencesBusy(false);
    }
  }

  async function changePageSize(size: number) {
    setPreferencesBusy(true);
    setPreferencesError("");
    try {
      await savePageSize(size);
      setPageSize(size);
    } catch (cause) {
      setPreferencesError(
        `Could not save preferences: ${cause instanceof Error ? cause.message : String(cause)}`,
      );
      throw cause;
    } finally {
      setPreferencesBusy(false);
    }
  }

  onMount(() => {
    // Browser storage must not gate the first render. A delayed preference
    // must also not overwrite a theme the user has already chosen.
    void loadTheme().then(
      (saved) => {
        if (!disposed && !themeChanged) setTheme(saved);
      },
      () => {
        if (!disposed && !themeChanged)
          setThemeError("Could not load the saved theme.");
      },
    );
    void loadSavedIndicators();
    void loadPreferences();
  });

  async function addIndicator(indicator: Indicator) {
    await saveIndicator(indicator);
    setIndicators((current) => [...current, indicator]);
    setIndicatorField(undefined);
  }

  async function removeIndicator(id: string) {
    await deleteIndicator(id);
    setIndicators((current) =>
      current.filter((indicator) => indicator.id !== id),
    );
  }

  createEffect(() => {
    tapRefresh();
    const abort = new AbortController();
    setTaps([]);
    setTapError("");
    setTapsLoading(true);
    void listTaps(token(), abort.signal)
      .then((names) => {
        if (!abort.signal.aborted) setTaps(names);
      })
      .catch((cause) => {
        if (!abort.signal.aborted)
          setTapError(cause instanceof Error ? cause.message : String(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) setTapsLoading(false);
      });
    onCleanup(() => abort.abort());
  });

  createEffect(() => {
    tapRefresh();
    const abort = new AbortController();
    setChoices({});
    setSchemaError("");
    setSchemaLoading(true);
    void loadDictionaries(tap(), token(), abort.signal)
      .then((dictionaries) => {
        if (!abort.signal.aborted)
          setChoices(dictionaries.choices(parameterNames));
      })
      .catch((cause) => {
        if (!abort.signal.aborted)
          setSchemaError(
            cause instanceof Error ? cause.message : String(cause),
          );
      })
      .finally(() => {
        if (!abort.signal.aborted) setSchemaLoading(false);
      });
    onCleanup(() => abort.abort());
  });

  async function restore(tokenValue: string) {
    stop();
    const current = ++generation,
      abort = new AbortController();
    controller = abort;
    working.cancel();
    working = new WorkingSet();
    working.setMatchers(matchers());
    collectionToken = tokenValue;
    collectionId = undefined;
    coverage = new Coverage();
    setCovered([]);
    setActiveSearch(undefined);
    activePredicate = undefined;
    setLocalParameters({});
    setLocalTap("");
    setLocalFrom("");
    setLocalTo("");
    setSelected(undefined);
    setRevealed(undefined);
    setHasCollected(false);
    setActiveSelection(undefined);
    setError("");
    setStatus("restoring");
    refresh();
    let last: string | undefined;
    try {
      last = sessionStorage.getItem("snoot-collection") ?? undefined;
    } catch {}
    try {
      await restoreCollection(
        tokenValue,
        last,
        abort.signal,
        (events) => {
          if (current !== generation) return;
          for (const event of events) queued.push(event);
          if (!frame) frame = requestAnimationFrame(flush);
        },
        (saved, ranges) => {
          if (current !== generation) return;
          collectionId = saved.id;
          setHasCollected(true);
          setActiveSelection(saved.selection);
          setTap(saved.selection.tap);
          setParameters(saved.parameters);
          setRange(saved.range);
          setFrom(saved.from);
          setTo(saved.to);
          setCollectionDraft(
            JSON.stringify([
              saved.selection.tap,
              saved.range,
              saved.from,
              saved.to,
              saved.parameters,
            ]),
          );
          setCollectionScope(
            describeSearch({
              parameters: saved.parameters,
              tap: saved.selection.tap,
              from: "",
              to: "",
            }) || "All taps · All fields",
          );
          // Coverage describes completed pulls, never inferred from event density.
          // Recent intervals remain reloadable for late arrivals.
          for (const range of ranges)
            coverage.add({
              ...range,
              to: Math.min(range.to, Date.now() - 15 * 60_000),
            });
          setCovered(coverage.intervals());
          try {
            sessionStorage.setItem("snoot-collection", saved.id);
          } catch {}
        },
      );
      if (current === generation) {
        flush();
        setStatus(collectionId ? "restored" : "idle");
      }
    } catch (cause) {
      if (current === generation && !abort.signal.aborted) {
        flush();
        setError(cause instanceof Error ? cause.message : String(cause));
        setStatus("error");
      }
    } finally {
      if (current === generation) controller = undefined;
    }
  }

  createEffect(() => {
    const credential = token();
    untrack(() => void restore(credential));
  });

  function refresh() {
    setCollected(working.collected.events.length);
    setAvailableWindow(collectionWindow(working.collected.events));
    setLocalChoices(working.choices());
    setRecords(working.visible.events);
    setAnnotations(working.visible.annotations);
    setSummary(working.visible.summary());
  }

  function flush() {
    cancelAnimationFrame(frame);
    frame = 0;
    if (queued.length) {
      if (working.append(queued)) refresh();
      queued = [];
    }
  }

  async function search(clear = false, expanded = false) {
    const selection = clear
      ? { parameters: {}, tap: "", from: "", to: "" }
      : localDraft();
    let predicate: Predicate | undefined;
    try {
      predicate = compileSearch(selection);
    } catch (cause) {
      setSearchError(cause instanceof Error ? cause.message : String(cause));
      return;
    }
    if (!clear && !expanded && offerExpansion(() => {}, true)) return;
    if (clear) {
      setLocalParameters({});
      setLocalTap("");
      setLocalFrom("");
      setLocalTo("");
    }
    const revision = ++searchRevision;
    setSearchError("");
    setSearching(true);
    if (
      (await working.search(predicate)) &&
      revision === searchRevision &&
      !disposed
    ) {
      activePredicate = predicate;
      setActiveSearch(predicate ? selection : undefined);
      setSelected(undefined);
      setRevealed(undefined);
      setViewRevision((value) => value + 1);
      refresh();
      setSearching(false);
    }
  }

  function stop() {
    setExpansion(undefined);
    generation++;
    controller?.abort();
    controller = undefined;
    flush();
    if (running()) setStatus("stopped");
  }

  function selection(follow: boolean): Selection {
    const now = new Date();
    let start: Date;
    let end: Date | undefined = follow ? undefined : now;
    if (range() === "custom") {
      if (!from())
        throw new Error("Choose a start time for the custom window.");
      start = new Date(`${from()}Z`);
      if (to()) end = new Date(`${to()}Z`);
    } else if (range() === "today") {
      start = new Date(now);
      start.setUTCHours(0, 0, 0, 0);
    } else {
      start = new Date(now.getTime() - Number(range()) * 60_000);
    }
    if (
      !Number.isFinite(start.getTime()) ||
      start.getTime() < 0 ||
      (end && (!Number.isFinite(end.getTime()) || end <= start))
    ) {
      throw new Error(
        "The end of the window must be after its start. Times use UTC.",
      );
    }
    return {
      tap: tap().trim(),
      from: start.toISOString(),
      ...(end ? { to: end.toISOString() } : {}),
      where: where(parameters()),
      limit: null,
      tail: false,
      follow,
      pretty: false,
    };
  }

  async function collect(follow = false) {
    let request: Selection;
    try {
      request = selection(follow);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      return;
    }
    stop();
    const current = ++generation;
    const abort = new AbortController();
    controller = abort;
    working.cancel();
    searchRevision++;
    setSearching(false);
    setLocalFrom("");
    setLocalTo("");
    setSearchError("");
    const previousSearch = activeSearch();
    if (previousSearch) {
      const retained = { ...previousSearch, from: "", to: "" };
      activePredicate = compileSearch(retained);
      setActiveSearch(activePredicate ? retained : undefined);
    }
    coverage = new Coverage();
    setCovered([]);
    setCollectionScope(
      describeSearch({
        parameters: parameters(),
        tap: request.tap,
        from: "",
        to: "",
      }) || "All taps · All fields",
    );
    collectionToken = token();
    working = new WorkingSet(activePredicate);
    working.setMatchers(matchers());
    setViewRevision((value) => value + 1);
    setCollectionDraft(collectionParameters());
    refresh();
    setSelected(undefined);
    setRevealed(undefined);
    setError("");
    setHasCollected(true);
    setActiveSelection(request);
    setStatus("connecting");
    try {
      collectionId = crypto.randomUUID();
      await beginCollection(
        {
          id: collectionId,
          created: Date.now(),
          selection: request,
          parameters: parameters(),
          range: range(),
          from: from(),
          to: to(),
        },
        collectionToken,
        abort.signal,
      );
      if (current !== generation) return;
      try {
        sessionStorage.setItem("snoot-collection", collectionId);
      } catch {}
      void navigator.storage?.persist?.().catch(() => false);
      await append(request, current, abort);
      if (current === generation) {
        flush();
        setStatus("ended");
      }
    } catch (cause) {
      if (current !== generation) return;
      flush();
      if (!abort.signal.aborted) {
        setError(cause instanceof Error ? cause.message : String(cause));
        setStatus("error");
      }
    } finally {
      if (current === generation) controller = undefined;
    }
  }

  async function append(
    request: Selection,
    current: number,
    abort: AbortController,
  ) {
    const started = Date.now();
    const complete = await streamEvents(
      request,
      collectionToken,
      abort.signal,
      (batch) => {
        if (current !== generation) return;
        for (const event of batch) queued.push(event);
        if (!frame) frame = requestAnimationFrame(flush);
      },
      () => {
        if (current === generation)
          setStatus(request.follow ? "following" : "reading");
      },
      (dictionaries) => {
        if (current === generation && tap() === request.tap)
          setChoices(dictionaries.choices(parameterNames));
      },
      collectionId,
    );
    if (complete && current === generation && !request.follow && request.to) {
      coverage.add({
        from: Date.parse(request.from),
        to: Math.min(Date.parse(request.to), started - 15 * 60_000),
      });
      setCovered(coverage.intervals());
    }
  }

  function eventInterval(): Interval | undefined {
    const window = availableWindow();
    if (window)
      return {
        from: Date.parse(`${window.from}Z`),
        to: Date.parse(`${window.to}Z`),
      };
    const request = activeSelection();
    if (request)
      return {
        from: Date.parse(request.from),
        to: request.to ? Date.parse(request.to) : Date.now(),
      };
  }

  function requestExpansion(
    window: TimeWindow,
    cancel: () => void,
    apply = false,
  ): boolean {
    const bounds = eventInterval();
    if (!bounds) return false;
    const from = window.from ? Date.parse(`${window.from}Z`) : bounds.from;
    const to = window.to ? Date.parse(`${window.to}Z`) : bounds.to;
    if (!Number.isFinite(from) || !Number.isFinite(to)) return false;
    const requested = {
      from: Math.min(from, bounds.from),
      to: Math.max(to, bounds.to),
    };
    if (requested.from < 0 || requested.to > Date.now()) {
      setSearchError(
        "Choose a time from the past. Follow live collects future events.",
      );
      return true;
    }
    const ranges = coverage.missing(requested);
    if (!ranges.length) return false;
    setExpansion({ ranges, cancel, apply });
    return true;
  }

  function offerExpansion(cancel: () => void, apply: boolean): boolean {
    if (!localFrom() && !localTo()) return false;
    return requestExpansion(
      { from: localFrom(), to: localTo() },
      cancel,
      apply,
    );
  }

  function changeWindow(window: TimeWindow, cancel: () => void) {
    const before = { from: localFrom(), to: localTo() };
    setLocalFrom(window.from);
    setLocalTo(window.to);
    setSearchError("");
    if (
      offerExpansion(() => {
        setLocalFrom(before.from);
        setLocalTo(before.to);
        cancel();
      }, true)
    )
      return;
    void search(false, true);
  }

  function cancelExpansion() {
    expansion()?.cancel();
    setExpansion(undefined);
  }

  async function expand() {
    const proposed = expansion();
    const original = activeSelection();
    if (!proposed || !original) return;
    stop();
    const current = ++generation;
    const abort = new AbortController();
    controller = abort;
    setError("");
    setSearchError("");
    setStatus("connecting");
    try {
      for (const range of proposed.ranges) {
        await append(
          {
            ...original,
            from: new Date(range.from).toISOString(),
            to: new Date(range.to).toISOString(),
            follow: false,
            limit: null,
            tail: false,
          },
          current,
          abort,
        );
        if (current !== generation) return;
        flush();
      }
      setStatus("ended");
      if (proposed.apply) await search(false, true);
    } catch (cause) {
      if (current !== generation) return;
      flush();
      if (!abort.signal.aborted) {
        setError(cause instanceof Error ? cause.message : String(cause));
        setStatus("error");
      }
    } finally {
      if (current === generation) controller = undefined;
    }
  }

  function navigate(next: Page) {
    setPage(next);
    setDrawer(false);
  }

  function revealEvent(event: LogEvent) {
    setSelected(undefined);
    setRevealed({ event });
    navigate("Search");
  }

  function narrow(
    scope: "Collect" | "Search",
    name: "service" | "level",
    value: string,
  ) {
    navigate("Search");
    if (scope === "Collect")
      setParameters((current) => ({ ...current, [name]: value }));
    else setLocalParameters((current) => ({ ...current, [name]: value }));
    queueMicrotask(() => {
      const card = scope === "Collect" ? collectionCard : localCard;
      card?.scrollIntoView({ block: "center", behavior: "smooth" });
      card
        ?.querySelector<HTMLSelectElement>(
          `select[aria-label="${scope} ${name}"]`,
        )
        ?.focus({ preventScroll: true });
    });
  }

  function openModal(kind: Panel) {
    setModal(kind);
  }

  function download() {
    const blob = new Blob(
      records().map((event) => `${jsonText(expandEvent(event))}\n`),
      { type: "application/x-ndjson" },
    );
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `snoot-${new Date().toISOString().replaceAll(":", "-")}.jsonl`;
    anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  async function copySelection() {
    try {
      await navigator.clipboard.writeText(
        JSON.stringify(activeSelection(), null, 2),
      );
      setCopied(true);
      clearTimeout(copyTimer);
      copyTimer = setTimeout(() => setCopied(false), 2000);
    } catch {
      setError(
        "Could not access the clipboard. Your browser may require permission.",
      );
    }
  }

  onCleanup(() => {
    disposed = true;
    working.cancel();
    searchRevision++;
    generation++;
    controller?.abort();
    cancelAnimationFrame(frame);
    clearTimeout(copyTimer);
  });

  return (
    <div
      class="workspace"
      classList={{ "drawer-open": drawer(), "sidebar-collapsed": collapsed() }}
      onKeyDown={(event) => {
        if (event.key === "Escape") setDrawer(false);
      }}
    >
      <Show when={drawer()}>
        <button
          class="drawer-scrim"
          aria-label="Close navigation"
          onClick={() => setDrawer(false)}
        />
      </Show>
      <Navigation
        theme={theme()}
        themeBusy={themeBusy()}
        themeError={themeError()}
        onToggleTheme={() => void toggleTheme()}
        page={page()}
        count={collected()}
        hasToken={Boolean(token())}
        onNavigate={navigate}
        onHelp={() => openModal("help")}
        onConnection={() => openModal("connection")}
      >
        <Show when={records().length}>
          <div class="rankings-column" aria-label="Operation rankings">
            <OperationRanking
              rows={summary().operations.errors}
              kind="errors"
              count={records().length}
            />
            <OperationRanking
              rows={summary().operations.latency}
              kind="latency"
              count={records().length}
            />
          </div>
        </Show>
      </Navigation>

      <main class="main">
        <header class="topbar">
          <button
            class="icon-button desktop-menu"
            aria-label={
              collapsed() ? "Expand navigation" : "Collapse navigation"
            }
            aria-expanded={!collapsed()}
            onClick={() => setCollapsed(!collapsed())}
          >
            <Icon name="panel" />
          </button>
          <button
            class="icon-button mobile-menu"
            aria-label="Open navigation"
            aria-expanded={drawer()}
            onClick={() => setDrawer(true)}
          >
            <Icon name="menu" />
          </button>
          <h1 class="content-brand">
            <img
              class="content-wordmark"
              src={wordmark}
              alt="snoot"
              width="2172"
              height="724"
            />
            <span class="sr-only"> — {page()}</span>
          </h1>
          <div class="topbar-right">
            <span class="topbar-caption">Your events, in focus.</span>
            <button class="guide-button" onClick={() => openModal("help")}>
              <Icon name="help" size={16} />
              <span>Quick guide</span>
            </button>
            <button
              class="profile-button"
              onClick={() => openModal("connection")}
              aria-label="Connection settings"
            >
              <Mark size={24} />
            </button>
          </div>
        </header>

        <div class="page-content">
          <Show when={page() === "Search"}>
            <form
              ref={collectionCard}
              class="card query-card"
              aria-label="Collect events"
              onSubmit={(event) => {
                event.preventDefault();
                void collect();
              }}
            >
              <div class="query-heading">
                <div>
                  <span class="query-symbol">
                    <Icon name="download" size={17} />
                  </span>
                  <strong>Collect</strong>
                </div>
                <span
                  class="scope-count"
                  aria-live="polite"
                  data-collected-count
                >
                  <strong>{collected().toLocaleString()}</strong> events
                  collected
                </span>
              </div>
              <Criteria
                scope="Collect"
                parameters={parameters()}
                onParameters={setParameters}
                choices={choices()}
              />
              <div class="query-controls">
                <label class="control tap-control">
                  <Icon name="leaf" size={15} />
                  <span class="sr-only">Tap</span>
                  <select
                    aria-label="Tap"
                    value={tap()}
                    onChange={(event) => setTap(event.currentTarget.value)}
                    aria-busy={tapsLoading()}
                  >
                    <option value="">All taps</option>
                    <Show when={tap() && !taps().includes(tap())}>
                      <option value={tap()}>{tap()}</option>
                    </Show>
                    <For each={taps()}>
                      {(name) => <option value={name}>{name}</option>}
                    </For>
                    <Show when={tapsLoading()}>
                      <option disabled>Loading taps…</option>
                    </Show>
                  </select>
                </label>
                <label class="control">
                  <Icon name="clock" size={15} />
                  <span class="sr-only">Time window</span>
                  <select
                    aria-label="Time window"
                    value={range()}
                    onChange={(event) => setRange(event.currentTarget.value)}
                  >
                    <option value="today">Today (UTC)</option>
                    <option value="15">Last 15 minutes</option>
                    <option value="60">Last hour</option>
                    <option value="1440">Last 24 hours</option>
                    <option value="10080">Last 7 days</option>
                    <option value="43200">Last 30 days</option>
                    <option value="custom">Custom window</option>
                  </select>
                </label>
                <span class="query-controls-spacer" />
                <button
                  type="button"
                  class={`follow-button ${activeSelection()?.follow && running() ? "is-following" : ""}`}
                  onClick={() =>
                    activeSelection()?.follow && running()
                      ? stop()
                      : void collect(true)
                  }
                >
                  <Icon
                    name={
                      activeSelection()?.follow && running()
                        ? "stop"
                        : "activity"
                    }
                    size={16}
                  />
                  {activeSelection()?.follow && running()
                    ? "Stop following"
                    : "Follow live"}
                </button>
                <Show when={running()}>
                  <button type="button" class="button secondary" onClick={stop}>
                    <Icon name="stop" size={14} />
                    Stop
                  </button>
                </Show>
                <button class="button primary" type="submit">
                  <Icon name="download" size={16} />
                  Collect
                </button>
              </div>
              <Show
                when={
                  schemaLoading() ||
                  (activeSelection() &&
                    collectionDraft() !== collectionParameters())
                }
              >
                <p class="scope-hint">
                  {activeSelection() &&
                  collectionDraft() !== collectionParameters()
                    ? "Parameters changed. Collect to replace the working set."
                    : ""}
                  {schemaLoading() ? " Loading field choices…" : ""}
                </p>
              </Show>
              <Show when={schemaError()}>
                <div class="tap-error" role="alert">
                  <span>Could not load field choices: {schemaError()}</span>
                  <button
                    type="button"
                    class="text-button"
                    onClick={() => setTapRefresh((value) => value + 1)}
                  >
                    Retry choices
                  </button>
                </div>
              </Show>
              <Show when={tapError()}>
                <div class="tap-error" role="alert">
                  <span>{tapError()}</span>
                  <button
                    type="button"
                    class="text-button"
                    onClick={() => setTapRefresh((count) => count + 1)}
                  >
                    Retry taps
                  </button>
                </div>
              </Show>
              <Show when={range() === "custom"}>
                <div class="custom-window">
                  <button
                    class="control time-window-trigger"
                    type="button"
                    aria-label="Collect time window"
                    onClick={() => setCalendar("Collect")}
                  >
                    <Icon name="clock" />
                    {windowLabel(from(), to())}
                  </button>
                  <Show when={to()}>
                    <button
                      class="text-button"
                      type="button"
                      onClick={() => setTo("")}
                    >
                      Open end
                    </button>
                  </Show>
                  <small>An open end means now, or live follow.</small>
                </div>
              </Show>
            </form>

            <Show when={error()}>
              <div class="notice error-notice" role="alert">
                <Icon name="bell" />
                <span>{error()}</span>
                <button
                  class="text-button"
                  onClick={() => openModal("connection")}
                >
                  Connection
                  <Icon name="arrow" size={14} />
                </button>
              </div>
            </Show>
            <form
              ref={localCard}
              class="card query-card local-search"
              aria-label="Search working set"
              aria-busy={searching()}
              onSubmit={(event) => {
                event.preventDefault();
                void search();
              }}
            >
              <div class="query-heading">
                <div>
                  <span class="query-symbol">
                    <Icon name="search" size={17} />
                  </span>
                  <strong>Search</strong>
                </div>
                <span class="scope-count" aria-live="polite" data-matched-count>
                  <strong>{records().length.toLocaleString()}</strong> of{" "}
                  {collected().toLocaleString()} events matched
                </span>
              </div>
              <Criteria
                scope="Search"
                parameters={localParameters()}
                onParameters={setLocalParameters}
                choices={localChoices()}
              />
              <div class="query-controls local-controls">
                <label class="control">
                  <span>Tap</span>
                  <select
                    aria-label="Search tap"
                    value={localTap()}
                    onChange={(event) => setLocalTap(event.currentTarget.value)}
                  >
                    <option value="">All collected taps</option>
                    <For each={taps()}>
                      {(name) => <option value={name}>{name}</option>}
                    </For>
                  </select>
                </label>
                <button
                  class="control time-window-trigger"
                  type="button"
                  aria-label="Search time window"
                  disabled={!hasCollected()}
                  onClick={() => setCalendar("Search")}
                >
                  <Icon name="clock" />
                  {windowLabel(
                    localFrom() || availableWindow()?.from,
                    localTo() || availableWindow()?.to,
                  )}
                </button>
                <span class="query-controls-spacer" />
                <Show
                  when={
                    activeSearch() ||
                    searchChanged() ||
                    searchError() ||
                    searching()
                  }
                >
                  <button
                    class="button secondary"
                    type="button"
                    onClick={() => void search(true)}
                  >
                    <Icon name="close" size={14} /> Clear search
                  </button>
                </Show>
                <button class="button primary" type="submit">
                  {searching() ? "Searching…" : "Search"}
                </button>
              </div>
              <Show when={searchError()}>
                <div class="tap-error" role="alert">
                  {searchError()} The current view is unchanged.
                </div>
              </Show>
            </form>

            <div class="search-layout">
              <div class="results-column">
                <Show when={records().length}>
                  <Activity
                    summary={summary()}
                    count={records().length}
                    onRemoveIndicator={removeIndicator}
                    onRevealEvent={revealEvent}
                  />
                </Show>
                <Show when={indicatorError()}>
                  <div class="indicator-error" role="alert">
                    {indicatorError()}{" "}
                    <button
                      class="text-button"
                      disabled={indicatorsLoading()}
                      onClick={() => void loadSavedIndicators()}
                    >
                      Retry indicators
                    </button>
                  </div>
                </Show>
                <Show when={preferencesError()}>
                  <div class="preferences-error" role="alert">
                    {preferencesError()}{" "}
                    <button
                      class="text-button"
                      disabled={preferencesBusy()}
                      onClick={() => void loadPreferences()}
                    >
                      Retry preferences
                    </button>
                  </div>
                </Show>
                <section class="card results-card">
                  <div class="results-toolbar">
                    <div class="view-tabs" aria-label="Results">
                      <span class="active">
                        Events <span>{records().length.toLocaleString()}</span>
                      </span>
                    </div>
                    <div class="results-actions">
                      <span
                        class="stream-status"
                        classList={{ live: running() }}
                      >
                        <i />
                        {statusLabel()}
                      </span>
                      <button
                        class="icon-button"
                        aria-label="Export matched events as JSONL"
                        title="Export JSONL"
                        disabled={!records().length}
                        onClick={download}
                      >
                        <Icon name="download" size={17} />
                      </button>
                    </div>
                  </div>
                  <Show
                    when={records().length}
                    fallback={
                      <div class="empty-state">
                        <div class="empty-art">
                          <span />
                          <Icon
                            name={running() ? "activity" : "search"}
                            size={29}
                          />
                          <span />
                        </div>
                        <h3>
                          {activeSearch() && collected()
                            ? "No events match this search"
                            : running()
                              ? "Listening for events"
                              : hasCollected()
                                ? error()
                                  ? "Let’s get you connected"
                                  : "No events in this window"
                                : "Every event tells a story"}
                        </h3>
                        <p>
                          {activeSearch() && collected()
                            ? `${collected().toLocaleString()} collected events are still available. Change or clear the search.`
                            : running()
                              ? "Waiting for matching events. Results will appear as they arrive."
                              : hasCollected()
                                ? error()
                                  ? "Check the connection details above, then collect again."
                                  : "Try collecting a wider time window or different fields."
                                : "Choose a tap and time window, then collect events to explore."}
                        </p>
                        <Show when={activeSearch()}>
                          <button
                            class="button secondary"
                            onClick={() => void search(true)}
                          >
                            Clear search
                          </button>
                        </Show>
                        <Show when={!hasCollected()}>
                          <button
                            class="button secondary"
                            onClick={() => {
                              void collect();
                            }}
                          >
                            Collect today
                            <Icon name="chevron" size={14} />
                          </button>
                          <button
                            class="text-button sample-action"
                            onClick={() => {
                              setTap("demo-week");
                              setRange("10080");
                              setParameters({});
                              void collect();
                            }}
                          >
                            Explore sample week
                            <Icon name="activity" size={14} />
                          </button>
                        </Show>
                      </div>
                    }
                  >
                    <Events
                      revision={viewRevision()}
                      events={records()}
                      selected={selected()}
                      select={(event) =>
                        setSelected(selected() === event ? undefined : event)
                      }
                      annotations={annotations()}
                      onIndicator={setIndicatorField}
                      indicatorsLoading={indicatorsLoading()}
                      reveal={revealed()}
                      pageSize={pageSize()}
                      onPageSize={changePageSize}
                      preferencesBusy={preferencesBusy()}
                    />
                  </Show>
                  <div class="results-footer">
                    <span>
                      {`${records().length.toLocaleString()} matched / ${collected().toLocaleString()} collected`}
                    </span>
                    <span>
                      Oldest → newest <span class="footer-dot">·</span> UTC
                    </span>
                  </div>
                </section>
                <div class="search-footnote">
                  <Icon name="link" size={14} />
                  <span>
                    {activeSelection()
                      ? `${activeSelection()!.tap || "All taps"} · ${activeSelection()!.from.replace("T", " ").replace(".000Z", " UTC")} → ${activeSelection()!.to?.replace("T", " ").replace(".000Z", " UTC") || "Live"}`
                      : "Collect from trie-trunk, then search the working set in this browser tab."}
                  </span>
                  <Show when={activeSelection()}>
                    <button
                      class="icon-button"
                      onClick={() => void copySelection()}
                      aria-label="Copy collection selection"
                      title="Copy selection"
                    >
                      <Icon name={copied() ? "check" : "copy"} size={14} />
                    </button>
                  </Show>
                </div>
              </div>
              <aside class="insights-column" aria-label="Search insights">
                <Overview summary={summary()} count={records().length} />
                <Breakdown
                  title="Services"
                  values={summary().services}
                  total={records().length}
                  onSelect={(scope, value) => narrow(scope, "service", value)}
                />
                <Breakdown
                  title="Levels"
                  values={summary().levels}
                  total={records().length}
                  levels
                  onSelect={(scope, value) => narrow(scope, "level", value)}
                />
              </aside>
            </div>
          </Show>

          <Show when={page() === "Dashboard"}>
            <div class="page-intro">
              <div>
                <span class="eyebrow">THE BIGGER PICTURE</span>
                <h2>A view of your signals.</h2>
                <p>A quick look at the matched subset of your working set.</p>
              </div>
              <button
                class="button secondary"
                onClick={() => navigate("Search")}
              >
                <Icon name="search" size={16} />
                Back to search
              </button>
            </div>
            <div class="notice search-notice">
              <span class="notice-icon">
                <Icon name="activity" size={17} />
              </span>
              <div>
                <strong>Your current result set, at a glance.</strong>
                <p>
                  These summaries cover {records().length.toLocaleString()}{" "}
                  matched events out of {collected().toLocaleString()}{" "}
                  collected. Search to explore this working set or Collect to
                  replace it.
                </p>
              </div>
            </div>
            <div class="dashboard-layout">
              <Activity
                summary={summary()}
                count={records().length}
                onRemoveIndicator={removeIndicator}
                onRevealEvent={revealEvent}
              />
              <Overview summary={summary()} count={records().length} />
              <Breakdown
                title="Services"
                values={summary().services}
                total={records().length}
                onSelect={(scope, value) => narrow(scope, "service", value)}
              />
              <Breakdown
                title="Levels"
                values={summary().levels}
                total={records().length}
                levels
                onSelect={(scope, value) => narrow(scope, "level", value)}
              />
            </div>
          </Show>

          <Show when={page() === "Alerts"}>
            <div class="page-intro">
              <div>
                <span class="eyebrow">STAY IN THE KNOW</span>
                <h2>Make the important things visible.</h2>
                <p>A home for the signals that deserve your attention.</p>
              </div>
              <span class="outline-badge">Not configured</span>
            </div>
            <section class="card alerts-empty">
              <div class="empty-art">
                <span />
                <Icon name="bell" size={30} />
                <span />
              </div>
              <h3>Good signals deserve a heads-up.</h3>
              <p>
                Alert rules and background evaluation aren’t connected yet. For
                now, follow a collection to watch events as they arrive.
              </p>
              <button class="button primary" onClick={() => navigate("Search")}>
                <Icon name="search" size={16} />
                Open search
              </button>
            </section>
          </Show>
          <footer class="page-footer">
            <span>
              <Icon name="leaf" size={13} /> Small footprint. Clear signals.
            </span>
            <span>snoot · powered by trie-trunk</span>
          </footer>
        </div>
      </main>

      <Show when={calendar()} keyed>
        {(scope) => (
          <Calendar
            scope={scope}
            from={
              scope === "Collect"
                ? from()
                : localFrom() ||
                  availableWindow()?.from ||
                  activeSelection()?.from.replace(/Z$/, "") ||
                  ""
            }
            to={
              scope === "Collect"
                ? to()
                : localTo() ||
                  availableWindow()?.to ||
                  activeSelection()?.to?.replace(/Z$/, "") ||
                  ""
            }
            coverage={scope === "Search" ? covered() : undefined}
            onClose={() => setCalendar(undefined)}
            onExpand={
              scope === "Search"
                ? (window, cancel) => {
                    requestExpansion(window, cancel);
                  }
                : undefined
            }
            onChange={(window, close, cancel) => {
              if (close) setCalendar(undefined);
              if (scope === "Collect") {
                setFrom(window.from);
                setTo(window.to);
              } else changeWindow(window, cancel);
            }}
          />
        )}
      </Show>
      <Show when={expansion()} keyed>
        {(proposal) => (
          <ExpansionDialog
            ranges={proposal.ranges}
            scope={`Original collection: ${collectionScope()}`}
            running={running()}
            onConfirm={() => void expand()}
            onClose={cancelExpansion}
          />
        )}
      </Show>
      <Settings
        panel={modal()}
        token={token()}
        onToken={setToken}
        onClose={() => setModal(undefined)}
      />
      <Show when={indicatorField()} keyed>
        {(field) => (
          <IndicatorDialog
            field={field}
            onSave={addIndicator}
            onClose={() => setIndicatorField(undefined)}
          />
        )}
      </Show>
    </div>
  );
}
