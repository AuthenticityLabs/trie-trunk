import { SearchPerformance } from './components/search-performance';
import { QueryEditor } from './components/query-editor';
import { parseQuery, type QueryField } from './services/query';
import { capturePosition, resolvePosition, validPosition, type ScrollPosition } from './services/ui-position';
import { readTab, writeTab } from "./services/search-session";
import { ViewInsights } from "./components/view-insights";
import { AlertCenter } from "./components/alerts";
import { PagedResults, type PagedViewState } from "./components/paged-results";
import { Histogram } from "./components/charts";
import type { ViewQuery } from "./services/pil/view";
import type { ObjectHistoryTarget } from './services/replay';
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
import { beginCollection, restoreCollection, rebaseCollection } from "./services/pil/client";
import wordmark from "./assets/snoot-wordmark.png";
import { Coverage, type Interval } from "./services/coverage";
import { ExpansionDialog } from "./components/expansion";
import { Events } from "./components/events";
import { Icon, ProfileMark } from "./components/icon";
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
  saveTheme,
  defaultTheme,
  nextTheme,
  themeColors,
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
  listCaches,
  type LogEvent,
  type Selection,
} from "./services/events";
import { windowLabel } from "./services/calendar";
import { WorkingSet } from "./services/working-set";

import { Sources } from "./components/sources";
import { resourceScope } from "./services/source";
import {
  collectionWindow,
  compileSearch,
  describeSearch,
  type Choices,
  type Parameters,
  type LocalSelection,
  type Predicate,
  type TimeWindow,
} from "./services/search";
import { expandEvent, jsonText } from "./services/dictionary";

const Calendar = lazy(() => import("./components/calendar"));
const DashboardMetrics = lazy(() => import("./components/dashboard"));
const Research = lazy(() => import("./components/research"));
const HistoryDialog = lazy(() => import("./components/research").then(module=>({default:module.HistoryDialog})));

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
type TabState = {
 queryText?:string; version:1; page:Page; collapsed:boolean; tap:string; range:string; from:string; to:string;
 localParameters:Parameters; localTap:string; localFrom:string; localTo:string;
 query?:ViewQuery; pageSize:number; scroll:number; positions?:Partial<Record<Page,ScrollPosition>>;
};
function savedTab():TabState|undefined {
 const state=readTab<TabState>('snoot-tab-v1');
 if(!state||state.version!==1||!['Search','Dashboard','Alerts','Object History'].includes(state.page)||
  (state.queryText!==undefined&&typeof state.queryText!=='string')||
  !Number.isFinite(state.scroll)||state.scroll<0||typeof state.collapsed!=='boolean'||
  (state.positions&&Object.values(state.positions).some(p=>!validPosition(p)))||
  !Number.isInteger(state.pageSize)||state.pageSize<1||state.pageSize>1000||
  !['tap','range','from','to','localTap','localFrom','localTo'].every(k=>typeof state[k as keyof TabState]==='string')||
  !state.localParameters||Object.values(state.localParameters).some(v=>typeof v!=='string')||
  (state.query&&(!Number.isFinite(Date.parse(state.query.from))||!Number.isFinite(Date.parse(state.query.to))||typeof state.query.tap!=='string')))return;
 return state;
}
export default function App() {
  const saved=savedTab();
  const [theme, setTheme] = createSignal<Theme>(defaultTheme);
  const [themeBusy, setThemeBusy] = createSignal(false);
  const [themeError, setThemeError] = createSignal("");
  let themeChanged = false;
  createEffect(() => {
    document.documentElement.dataset.theme = theme();
    document.querySelector('meta[name="theme-color"]')?.setAttribute(
      "content",
      themeColors[theme()],
    );
  });

  async function toggleTheme() {
    if (themeBusy()) return;
    themeChanged = true;
    const next = nextTheme(theme());
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
  const [calendarPaged,setCalendarPaged]=createSignal(false);
  const [calendarBounds,setCalendarBounds]=createSignal<TimeWindow>({from:'',to:''});
  const [covered, setCovered] = createSignal<readonly Interval[]>([]);
  const [collectionScope, setCollectionScope] = createSignal("");
  const [expansion, setExpansion] = createSignal<{
    ranges: Interval[];
    cancel: () => void;
    apply: boolean;
  }>();
  let activePredicate: Predicate | undefined;
  const [page, setPage] = createSignal<Page>(saved?.page ?? "Search");
  const [drawer, setDrawer] = createSignal(false);
  const [collapsed, setCollapsed] = createSignal(saved?.collapsed ?? false);
  const [modal, setModal] = createSignal<Panel>();
  const [token, setToken] = createSignal("");
  const [tap, setTap] = createSignal(resourceScope(saved?.tap ?? ""));
  const [taps, setTaps] = createSignal<string[]>([]);
  const [collections,setCollections]=createSignal<string[]>([]);
  const [historyObject,setHistoryObject]=createSignal<ObjectHistoryTarget>();
  function openObjectHistory(target:ObjectHistoryTarget){setHistoryObject(target);}
  const [tapError, setTapError] = createSignal("");
  const [tapsLoading, setTapsLoading] = createSignal(false);
  const [tapRefresh, setTapRefresh] = createSignal(0);
  const [range, setRange] = createSignal(saved?.range ?? "today");
  const [from, setFrom] = createSignal(saved?.from ?? "");
  const [to, setTo] = createSignal(saved?.to ?? "");
  const [queryText,setQueryText]=createSignal(saved?.queryText??'');
  const [queryFields,setQueryFields]=createSignal<QueryField[]>([]);
  const [choices, setChoices] = createSignal<Choices>({});
  const [schemaError, setSchemaError] = createSignal("");
  const [schemaLoading, setSchemaLoading] = createSignal(false);
  const [localParameters, setLocalParameters] = createSignal<Parameters>(saved?.localParameters ?? {});
  const [localChoices, setLocalChoices] = createSignal<Choices>({});
  const [localTap, setLocalTap] = createSignal(saved?.localTap ?? "");
  const [localFrom, setLocalFrom] = createSignal(saved?.localFrom ?? "");
  const [localTo, setLocalTo] = createSignal(saved?.localTo ?? "");
  const [activeSearch, setActiveSearch] = createSignal<LocalSelection>();
  const [searchError, setSearchError] = createSignal("");
  const [searching, setSearching] = createSignal(false);
  const [collected, setCollected] = createSignal(0);
  const [availableWindow, setAvailableWindow] = createSignal<TimeWindow>();
  const [viewRevision, setViewRevision] = createSignal(0);
  const [collectionDraft, setCollectionDraft] = createSignal("");
  const collectionParameters = () =>
    JSON.stringify([tap(), range(), from(), to()]);
  const localDraft = (): LocalSelection => ({
    parameters: Object.fromEntries(
      Object.entries(localParameters())
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
  const [pagedQuery,setPagedQuery]=createSignal<ViewQuery | undefined>(saved?.query);
  const [pagedView,setPagedView]=createSignal<PagedViewState>({loaded:0,busy:false,error:""});
  const [pageSize, setPageSize] = createSignal(saved?.pageSize ?? defaultPageSize);
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
      if (!disposed && !saved) setPageSize(size);
    } catch (cause) {
      if (!disposed)
        setPreferencesError(
          `Could not load preferences: ${cause instanceof Error ? cause.message : String(cause)}`,
        );
    } finally {
      if (!disposed) setPreferencesBusy(false);
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
    setCollections([]);setHistoryObject(undefined);
    setTapError("");
    setTapsLoading(true);
    void listCaches(token(), abort.signal, sources=>{if(!abort.signal.aborted)setCollections(sources)})
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
    setSchemaLoading(true);setQueryFields([]);
    void loadDictionaries(tap(), token(), abort.signal)
      .then((dictionaries) => {
        if (!abort.signal.aborted){
          setChoices(dictionaries.choices());setQueryFields(dictionaries.fields());}
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
    setPagedQuery(undefined);
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
          setTap(resourceScope(saved.selection.tap));
          setRange(saved.range);
          setFrom(saved.from);
          setTo(saved.to);
          setCollectionDraft(
            saved.selection.where.length ? "" : JSON.stringify([
              saved.selection.tap,
              saved.range,
              saved.from,
              saved.to,
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

  let initialRestore=true;
  createEffect(() => {
    const credential = token();
    if(initialRestore&&saved?.query){initialRestore=false;return;}
    initialRestore=false;
    untrack(() => void restore(credential));
  });

  let tabState:TabState;
  const positions:Partial<Record<Page,ScrollPosition>>={...saved?.positions};
  if(saved&&!positions[saved.page])positions[saved.page]={top:saved.scroll,left:0};
  let scrollPending=false;
  let stopScrollRestore=()=>{};
  const saveTab=()=>{if(tabState)writeTab('snoot-tab-v1',{...tabState,positions,scroll:positions[page()]?.top??0})};
  const captureScroll=()=>{
    const el=document.scrollingElement;
    if(el&&!scrollPending)positions[page()]=capturePosition(el);
    saveTab();
  };
  createEffect(()=>{
    tabState={queryText:queryText(),version:1,page:page(),collapsed:collapsed(),tap:tap(),range:range(),from:from(),to:to(),
      localParameters:localParameters(),localTap:localTap(),localFrom:localFrom(),localTo:localTo(),
      query:pagedQuery(),pageSize:pageSize(),scroll:positions[page()]?.top??0};
    saveTab();
  });
  onMount(()=>{
    history.scrollRestoration='manual';
    let timer:ReturnType<typeof setTimeout>|undefined;
    const scroll=()=>{clearTimeout(timer);timer=setTimeout(captureScroll,150)};
    const flush=()=>{clearTimeout(timer);captureScroll()};
    addEventListener('scroll',scroll,{passive:true});addEventListener('pagehide',flush);
    onCleanup(()=>{clearTimeout(timer);removeEventListener('scroll',scroll);removeEventListener('pagehide',flush)});
  });
  createEffect(()=>{
    const current=page();
    const target=positions[current]??{top:0,left:0};
    stopScrollRestore();scrollPending=true;
    let frame=0;
    const stop=()=>{cancelAnimationFrame(frame);observer.disconnect();clearTimeout(timeout);scrollPending=false;
      removeEventListener('wheel',stop);removeEventListener('touchstart',stop);removeEventListener('keydown',stop)};
    const restore=()=>{cancelAnimationFrame(frame);frame=requestAnimationFrame(()=>{
      const el=document.scrollingElement;if(!el)return;
      if((current==='Search'||current==='Dashboard')&&pagedQuery()&&!pagedView().result)return;
      // Lazy pages and restored search rows must render before clamping the position.
      const position=resolvePosition(target,el);
      window.scrollTo(position.left,position.top);
      if(el.scrollHeight-el.clientHeight>=position.top&&Math.abs(el.scrollTop-position.top)<2)stop();
    })};
    const observer=new ResizeObserver(restore);
    observer.observe(document.body);
    const timeout=setTimeout(stop,5000);
    addEventListener('wheel',stop,{passive:true});addEventListener('touchstart',stop,{passive:true});addEventListener('keydown',stop);
    stopScrollRestore=stop;restore();onCleanup(stop);
  });

  function refresh() {
    setViewRevision((value) => value + 1);
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
  async function rebuildLocalCollection(source:string){
    stop();
    const current=generation,credential=token();
    const abort=new AbortController();
    await rebaseCollection(source,credential,abort.signal);
    if(current!==generation||credential!==token())return;
    working.cancel();searchRevision++;setSearching(false);
    working=new WorkingSet();working.setMatchers(matchers());
    activePredicate=undefined;setActiveSearch(undefined);setSearchError("");
    coverage=new Coverage();setCovered([]);collectionId=undefined;
    setSelected(undefined);setRevealed(undefined);setHasCollected(false);setActiveSelection(undefined);setCollectionDraft("");
    setError("");setStatus("idle");refresh();
    try{sessionStorage.removeItem("snoot-collection");}catch{}
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
      where: [],
      limit: null,
      tail: false,
      follow,
      pretty: false,
    };
  }

  function searchWindow():TimeWindow {
    if(!pagedQuery()&&hasCollected())return {from:localFrom()||activeSelection()?.from.replace(/Z$/,'')||availableWindow()?.from||'',to:localTo()||activeSelection()?.to?.replace(/Z$/,'')||availableWindow()?.to||''};
    try{const q=selection(false);return {from:q.from.replace(/Z$/,''),to:q.to?.replace(/Z$/,'')||''}}catch{return {from:from(),to:to()}}
  }
  function openSearchWindow(){setCalendarBounds(searchWindow());setCalendarPaged(!!pagedQuery()||!hasCollected());setCalendar('Search')}

  async function submitSearch() {
    try {
      const ast=parseQuery(queryText(),queryFields());
      for(const [field,value] of Object.entries(localParameters()))if(value)ast.plan.filters.push({field,op:'==',value:{kind:'string',value}});
      const q=selection(false);stop();setError("");setSearchError("");
      setPagedQuery({tap:q.tap,from:q.from,to:q.to!,size:ast.page?.size??pageSize(),after:ast.page?.after,
        ...(ast.plan.filters.length||ast.plan.search||ast.plan.order!=='asc'?{query:ast.plan}:{})});
    }catch(e){setSearchError(e instanceof Error?e.message:String(e))}
  }

  async function collect(follow = false) {
    setPagedQuery(undefined);
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
    const retained = { ...localDraft(), from: "", to: "", tap: "" };
    setLocalTap("");
    activePredicate = compileSearch(retained);
    setActiveSearch(activePredicate ? retained : undefined);
    coverage = new Coverage();
    setCovered([]);
    setCollectionScope(
      describeSearch({
        parameters: {},
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
          parameters: {},
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
          setChoices(dictionaries.choices());
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
        "Choose a time from the past. Follow Live collects future events.",
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
    captureScroll();
    setPage(next);
    setDrawer(false);
  }

  function revealEvent(event: LogEvent) {
    setSelected(undefined);
    setRevealed({ event });
    navigate("Search");
  }

  function searchChart(from?: number, to?: number) {
    // Drill-down retains the applied parameters, not unsubmitted form edits.
    const selection = activeSearch();
    setLocalParameters(selection?.parameters ?? {});
    setLocalTap(selection?.tap ?? "");
    setLocalFrom(from === undefined ? "" : new Date(from).toISOString().slice(0, -1));
    setLocalTo(to === undefined ? "" : new Date(to).toISOString().slice(0, -1));
    void search(false, true);
  }

  function narrow(
    scope: "Collect" | "Search",
    name: "service" | "level",
    value: string,
  ) {
    navigate("Search");
    setLocalParameters((current) => ({ ...current, [name]: value }));
    void search(false, true);
    queueMicrotask(() => {
      collectionCard?.scrollIntoView({ block: "center", behavior: "smooth" });
      collectionCard?.querySelector<HTMLTextAreaElement>('#snoot-query')
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
          aria-label="Close Navigation"
          onClick={() => setDrawer(false)}
        />
      </Show>
      <Navigation
        theme={theme()}
        themeBusy={themeBusy()}
        themeError={themeError()}
        onToggleTheme={() => void toggleTheme()}
        page={page()}
        count={pagedQuery()?pagedView().result?.summary?.count??0:collected()}
        hasToken={Boolean(token())}
        onNavigate={navigate}
        onHelp={() => openModal("help")}
        onConnection={() => openModal("connection")}
      >
        <Show when={pagedQuery() ? (pagedView().result?.summary?.count??0)>0 && !!pagedView().result?.summary?.statistics : records().length>0}>
          <div class="rankings-column" aria-label="Operation Rankings">
            <OperationRanking
              rows={pagedQuery()?pagedView().result!.summary!.statistics!.operations.errors:summary().operations.errors}
              kind="errors"
              count={pagedQuery()?pagedView().result?.summary?.count??0:records().length}
            />
            <OperationRanking
              rows={pagedQuery()?pagedView().result!.summary!.statistics!.operations.latency:summary().operations.latency}
              kind="latency"
              count={pagedQuery()?pagedView().result?.summary?.count??0:records().length}
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
            aria-label="Open Navigation"
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
              <span>Quick Guide</span>
            </button>
            <button
              class="profile-button"
              onClick={() => openModal("connection")}
              aria-label="Connection Settings"
            >
              <ProfileMark size={24} />
            </button>
          </div>
        </header>

        <div class="page-content">
          {/* Preserve the current page and snapshot across navigation. */}
          <div style={{display:page() === "Search"?"contents":"none"}}>
            <form
              ref={collectionCard}
              class="card query-card"
              aria-label="Search Events"
              onSubmit={(event) => {
                event.preventDefault();
                void submitSearch();
              }}
            >
              <div class="query-heading">
                <div>
                  <span class="query-symbol">
                    <Icon name="search" size={17} />
                  </span>
                  <strong>Search</strong>
                </div>
                <span class="scope-count" aria-live="polite">
                  <span data-matched-count><strong>{pagedQuery()?(pagedView().result?.summary?.count.toLocaleString()??"—"):records().length.toLocaleString()}</strong> matched</span>
                  {" · "}
                  <span data-collected-count><strong>{(pagedQuery()?pagedView().loaded:collected()).toLocaleString()}</strong> {pagedQuery()?"loaded":"collected"}</span>
                  <Show when={pagedQuery() && pagedView().timing}>{" · "}<SearchPerformance timing={pagedView().timing} /></Show>
                </span>
              </div>
              <Sources value={tap()} catalog={taps()} loading={tapsLoading()} onChange={setTap} />
              <Show when={Object.values(localParameters()).some(Boolean)}><div class="query-filter-chips"><For each={Object.entries(localParameters()).filter(([,value])=>!!value)}>{([field,value])=><button type="button" class="text-button" aria-label={`Remove ${field} filter`} onClick={()=>setLocalParameters(current=>{const next={...current};delete next[field];return next})}>{field} == {JSON.stringify(value)} ×</button>}</For></div></Show>
              <QueryEditor value={queryText()} onInput={setQueryText} fields={queryFields()} loading={schemaLoading()} />
              <div class="query-controls">
                <label class="control">
                  <Icon name="clock" size={15} />
                  <span class="sr-only">Time Window</span>
                  <select aria-label="Time Window" value={range()} onChange={event=>setRange(event.currentTarget.value)}>
                    <option value="today">Today (UTC)</option>
                    <option value="15">Last 15 Minutes</option>
                    <option value="60">Last Hour</option>
                    <option value="1440">Last 24 Hours</option>
                    <option value="10080">Last 7 Days</option>
                    <option value="43200">Last 30 Days</option>
                    <option value="86400">Last 60 Days</option>
                    <option value="custom">Custom Window</option>
                  </select>
                </label>
                <button class="control time-window-trigger" type="button" aria-label="Search Time Window" onClick={openSearchWindow}>
                  <Icon name="clock" />{windowLabel(searchWindow().from,searchWindow().to)}
                </button>
                <span class="query-controls-spacer" />
                <button
                  type="button"
                  disabled={!!queryText().trim()}
                  title={queryText().trim()?"Text queries currently use snapshot search":""}
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
                    ? "Stop Following"
                    : "Follow Live"}
                </button>
                <Show when={running()}>
                  <button type="button" class="button secondary" onClick={stop}>
                    <Icon name="stop" size={14} />
                    Stop
                  </button>
                </Show>
                <Show when={queryText() || activeSearch() || searchChanged() || searchError() || searching()}>
                  <button class="button secondary" type="button" onClick={() => {setQueryText("");setLocalParameters({});void submitSearch()}}>
                    <Icon name="close" size={14} /> Clear
                  </button>
                </Show>
                <Show when={hasCollected() && !running() && !pagedQuery()}>
                  <button class="text-button" type="button" onClick={() => void collect()}
                    title="Check the server for new events and missing ranges">Refresh</button>
                </Show>
                <button class="button primary" type="submit" disabled={searching()}>
                  <Icon name="search" size={16} />
                  {searching() ? "Searching…" : "Search"}
                </button>
              </div>
              <Show when={searchError()}>
                <div class="tap-error" role="alert">{searchError()} The current view is unchanged.</div>
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
                    Retry sources
                  </button>
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

            <Show when={pagedQuery()} keyed>{query=><PagedResults query={query} token={token()} pageSize={query.size} active={page()==="Search"}
              matchers={matchers()} onIndicator={setIndicatorField} indicatorsLoading={indicatorsLoading()}
              collections={collections()} onObjectHistory={openObjectHistory}
              onState={setPagedView} />}</Show>
            <Show when={!pagedQuery()}><div class="search-layout">
              <div class="results-column">
                <Show when={records().length}>
                  <Activity
                    summary={summary()}
                    count={records().length}
                    onWindow={searchChart}
                    onResetWindow={() => searchChart()}
                    hasWindow={!!(activeSearch()?.from || activeSearch()?.to)}
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
                                  ? "Check the connection details above, then search again."
                                  : "Try a wider time window or different resource."
                                : "Choose a Resource and time window to search."}
                        </p>
                        <Show when={activeSearch()}>
                          <button
                            class="button secondary"
                            onClick={() => {setQueryText("");setLocalParameters({});void submitSearch()}}
                          >
                            Clear
                          </button>
                        </Show>
                        <Show when={!hasCollected()}>
                          <button
                            class="button secondary"
                            onClick={() => {
                              void collect();
                            }}
                          >
                            Search today
                            <Icon name="chevron" size={14} />
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
                      collections={collections()} onObjectHistory={openObjectHistory}
                      pageSize={pageSize()}

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
                      aria-label="Copy Collection Selection"
                      title="Copy Selection"
                    >
                      <Icon name={copied() ? "check" : "copy"} size={14} />
                    </button>
                  </Show>
                </div>
              </div>
              <aside class="insights-column" aria-label="Search Insights">
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
            </div></Show>
          </div>

          <Show when={page() === "Object History" ? "Object History" as const : undefined} keyed>
            {mode=><Research mode={mode} token={token()} sources={collections()} initialSource={tap()} pageSize={pageSize()} onIndicator={setIndicatorField} indicatorsLoading={indicatorsLoading()}/>}
          </Show>
          <Show when={page() === "Dashboard"}>
            <div class="page-intro">
              <div>
                <span class="eyebrow">THE BIGGER PICTURE</span>
                <h2>Signal Overview</h2>
                <p>{pagedQuery()?"Summaries across the complete search window.":"A quick look at the matched subset of your working set."}</p>
              </div>
              <button
                class="button secondary"
                onClick={() => navigate("Search")}
              >
                <Icon name="search" size={16} />
                Back to Search
              </button>
            </div>
            <Show when={pagedQuery()} fallback={<>
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
              <div class="dashboard-main">
                <Activity
                  summary={summary()}
                  count={records().length}
                  onWindow={searchChart}
                  onResetWindow={() => searchChart()}
                  hasWindow={!!(activeSearch()?.from || activeSearch()?.to)}
                  onRemoveIndicator={removeIndicator}
                  onRevealEvent={revealEvent}
                />
                <DashboardMetrics
                  records={records()}
                  revision={viewRevision()}
                  onWindow={searchChart}
                />
              </div>
              <aside class="dashboard-sidebar">
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
            </>}>
              <Show when={pagedView().error}><div class="notice error-notice" role="alert">{pagedView().error}</div></Show>
              <Show when={pagedView().result?.summary} fallback={<p role="status">{pagedView().busy?"Loading search summary…":"No search summary available."}</p>}>
                {s=><div class="dashboard-layout" data-paged-dashboard>
                  <section class="card activity-card">
                    <div class="results-toolbar"><strong>Event Activity</strong><span>{s().count.toLocaleString()} matched</span><SearchPerformance timing={pagedView().timing} /></div>
                    <Histogram counts={s().counts} errors={s().errors} first={s().from} last={s().from+s().step*s().counts.length} count={s().count}
                      onWindow={(from,to)=>{const q=pagedQuery()!;setPagedQuery({...q,from:new Date(Math.max(from,s().from)).toISOString(),to:new Date(Math.min(to,s().to)).toISOString()});navigate("Search")}} />
                    <Show when={pagedView().result?.provisional}><div class="results-footer">Committed events only · the recent arrival window is still open</div></Show>
                  </section>
                  <ViewInsights summary={s()} loaded={pagedView().loaded} dashboard />
                </div>}
              </Show>
            </Show>
          </Show>

          <Show when={page() === "Alerts"}>
            <AlertCenter token={token()} lostEvents={()=><Research mode="Lost Events" token={token()} sources={taps()} initialSource={tap()} pageSize={pageSize()} onIndicator={setIndicatorField} indicatorsLoading={indicatorsLoading()}/>}>
            <div class="page-intro">
              <div>
                <span class="eyebrow">STAY IN THE KNOW</span>
                <h2>Alerts</h2>
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
              <h3>Alert Rules</h3>
              <p>
                Alert rules and background evaluation aren’t connected yet. For
                now, follow a resource to watch events as they arrive.
              </p>
              <button class="button primary" onClick={() => navigate("Search")}>
                <Icon name="search" size={16} />
                Open Search
              </button>
            </section>
            </AlertCenter>
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
            from={calendarPaged()?calendarBounds().from:scope === 'Collect'?from():localFrom()||activeSelection()?.from.replace(/Z$/,'')||availableWindow()?.from||''}
            to={calendarPaged()?calendarBounds().to:scope === 'Collect'?to():localTo()||activeSelection()?.to?.replace(/Z$/,'')||availableWindow()?.to||''}
            coverage={scope === 'Search'?(calendarPaged()?[]:covered()):undefined}
            onClose={() => setCalendar(undefined)}
            onExpand={
              scope === "Search" && !calendarPaged()
                ? (window, cancel) => {
                    requestExpansion(window, cancel);
                  }
                : undefined
            }
            onChange={(window, close, cancel) => {
              if (close) setCalendar(undefined);
              if (calendarPaged() || scope === "Collect") {
                setRange("custom");
                setFrom(window.from);
                setTo(window.to);
                void submitSearch();
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
      <Show when={historyObject()} keyed>{object=><HistoryDialog token={token()} sources={collections()} initialSource={object.source}
        initialObject={object} pageSize={pageSize()} onIndicator={setIndicatorField} indicatorsLoading={indicatorsLoading()}
        onClose={()=>setHistoryObject(undefined)}/>}</Show>
      <Settings
        panel={modal()}
        token={token()}
        onToken={setToken}
        sources={taps()}
        onRebase={rebuildLocalCollection}
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
