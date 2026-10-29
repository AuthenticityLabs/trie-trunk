import { createAbortScope } from './services/abort';
import {zonedTimestamp,parseZonedTimestamp} from './services/time-zone';
import { restoreWorkspace, type TabState } from './services/search-workspace';
import { SummarySubscriptions } from "./components/summary-subscriptions";
import { SearchPerformance } from './components/search-performance';
import { QueryEditor } from './components/query-editor';
import { parseQuery, type QueryField } from './services/query';
import { capturePosition, resolvePosition, type ScrollPosition } from './services/ui-position';
import { readTab, writeTab, clearTabWorkspace } from "./services/search-session";
import { ViewInsights } from "./components/view-insights";
import { AlertCenter } from "./components/alerts";
import { PagedResults, type PagedViewState } from "./components/paged-results";
import {TimeActivity} from './components/time-activity';
import { Histogram } from "./components/charts";
import type { ViewQuery } from "./services/pil/view";
import type { ObjectHistoryTarget } from './services/replay';
import type { DiagnosticTarget } from './services/diagnostics';
import {
  For,
  Show,
  batch,
  createEffect,
  createComputed,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
  lazy,
  untrack,
} from "solid-js";
import { beginCollection, restoreCollection, disconnectClient,loadTrunk,selectConnection } from "./services/pil/client";
import {defaultProfile,loadProfile,saveProfile,type UserProfile} from './services/profile';
import {localConnection,connectionScope,type ServerConnection} from './services/connections';
import {Choices as DefinitionChoices} from './services/pil/metadata';
import type {TrunkBootstrap} from './services/pil/trunk';
import type {ProfileTab} from './components/profile';
import wordmark from "./assets/snoot-wordmark.png";
import { Coverage, type Interval } from "./services/coverage";
import { ExpansionDialog } from "./components/expansion";
import { Events } from "./components/events";
import { Icon } from "./components/icon";
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
  saveTheme,
  defaultTheme,
  nextTheme,
  themeColors,
  resolveTheme,
  type ThemePreference,
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
  streamEvents,
  type LogEvent,
  type Selection,
} from "./services/events";
import { windowLabel } from "./services/calendar";
import { WorkingSet } from "./services/working-set";

import { Sources } from "./components/sources";
import { resourceScope,matchesSource } from "./services/source";
import {
  collectionWindow,
  compileSearch,
  describeSearch,
  type Parameters,
  type LocalSelection,
  type Predicate,
  type TimeWindow,
} from "./services/search";
import { expandEvent, jsonText } from "./services/dictionary";

const Calendar = lazy(() => import("./components/calendar"));
const Configurations = lazy(() => import("./components/configurations"));
const DashboardWorkspace = lazy(() => import("./components/dashboard-workspace"));
const DashboardMetrics = lazy(() => import("./components/dashboard"));
const Research = lazy(() => import("./components/research"));
const HistoryDialog = lazy(() => import("./components/research").then(module=>({default:module.HistoryDialog})));
const DiagnosticDialog = lazy(() => import('./components/diagnostics'));
const Profile = lazy(() => import('./components/profile'));

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
  const [signedOut, setSignedOut] = createSignal(readTab<boolean>('snoot-signed-out') === true);
  const leave = () => {
    setSignedOut(true);
    disconnectClient();
    clearTabWorkspace();
    writeTab('snoot-signed-out', true);
  };
  const resume = () => {
    clearTabWorkspace();
    writeTab('snoot-signed-out', false);
    setSignedOut(false);
  };
  return <Show when={!signedOut()} fallback={<main class="signed-out"><img class="content-wordmark" src={wordmark} alt="snoot"/><h1>Logged Out of This Tab</h1><button class="button" onClick={resume}>Open Workspace</button></main>}><Workspace onLogout={leave}/></Show>;
}

function Workspace(props: {onLogout: () => void}) {
  const saved=restoreWorkspace(readTab('snoot:workspace'));
  const [profile,setProfile]=createSignal<UserProfile>(defaultProfile());
  const [profileReady,setProfileReady]=createSignal(false);
  const [profileTab,setProfileTab]=createSignal<ProfileTab>((['preferences','connections','storage'].includes(sessionStorage.getItem('snoot:profile:tab')??'')?sessionStorage.getItem('snoot:profile:tab'):'preferences') as ProfileTab);
  createEffect(()=>sessionStorage.setItem('snoot:profile:tab',profileTab()));
  const storageResets=new BroadcastChannel('snoot-storage-reset');
  storageResets.onmessage=event=>{if(event.data==='reload')location.reload()};
  onCleanup(()=>storageResets.close());
  const [activeServer,setActiveServer]=createSignal(saved?.connection??'local');
  const [connectionTokens,setConnectionTokens]=createSignal<Record<string,string>>({});
  const activeRegistration=createMemo(()=>profile().connections.find(c=>c.enabled&&c.id===activeServer()));
  const [themePreference, setThemePreference] = createSignal<ThemePreference>(defaultTheme);
  const systemAppearance = matchMedia('(prefers-color-scheme: dark)');
  const [systemDark, setSystemDark] = createSignal(systemAppearance.matches);
  const appearanceChanged = (event: MediaQueryListEvent) => setSystemDark(event.matches);
  systemAppearance.addEventListener('change', appearanceChanged);
  onCleanup(() => systemAppearance.removeEventListener('change', appearanceChanged));
  const theme = createMemo(() => {const value=resolveTheme(themePreference(),systemDark());return value==='trunk'&&profile().palette==='snoot'?'dark':value;});
  const [themeBusy, setThemeBusy] = createSignal(false);
  const [themeError, setThemeError] = createSignal("");
  let themeChanged = false;
  createEffect(() => {
    document.documentElement.dataset.theme = theme();
    document.documentElement.dataset.palette=profile().palette;
    document.querySelector('meta[name="theme-color"]')?.setAttribute(
      "content",
      themeColors[theme()],
    );
  });

  async function chooseTheme(next: ThemePreference) {
    if (themeBusy()||!profileReady()) return;
    themeChanged = true;
    setThemePreference(next);
    setThemeBusy(true);
    setThemeError("");
    try {
      await saveTheme(next);
      const value=await saveProfile({...profile(),appearance:next});setProfile(value);
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
  const managingStorage=createMemo(()=>page()==='Profile'&&profileTab()==='storage');
  const [searchView, setSearchView] = createSignal<'events'|'charts'>(saved?.searchView ?? 'events');
  const [drawer, setDrawer] = createSignal(false);
  const [collapsed, setCollapsed] = createSignal(saved?.collapsed ?? false);
  const [modal, setModal] = createSignal<Panel>();
  const token=createMemo(()=>connectionTokens()[activeServer()]??'');
  const contextKey=createMemo(()=>connectionScope(activeRegistration()??{...localConnection(),id:activeServer()||'disconnected'},token()));
  createComputed(()=>selectConnection(activeRegistration()??localConnection()));
  const [tap, setTap] = createSignal(resourceScope(saved?.tap ?? ""));
  const [taps, setTaps] = createSignal<string[]>([]);
  const [collections,setCollections]=createSignal<string[]>([]);
  const [historyObject,setHistoryObject]=createSignal<ObjectHistoryTarget>();
  function openObjectHistory(target:ObjectHistoryTarget){setHistoryObject(target);}
  const [diagnostic,setDiagnostic]=createSignal<DiagnosticTarget>();
  const [tapError, setTapError] = createSignal("");
  const [tapsLoading, setTapsLoading] = createSignal(false);
  const [tapRefresh, setTapRefresh] = createSignal(0);
  const [range, setRange] = createSignal(saved?.range ?? "today");
  const [from, setFrom] = createSignal(saved?.from ?? "");
  const [to, setTo] = createSignal(saved?.to ?? "");
  const [queryText,setQueryText]=createSignal(saved?.queryText??'');
  const [queryFields,setQueryFields]=createSignal<QueryField[]>([]);
  const [schemaError, setSchemaError] = createSignal("");
  const [schemaLoading, setSchemaLoading] = createSignal(false);
  const queryValid = createMemo(() => {
    try { parseQuery(queryText(), queryFields()); return true; }
    catch { return false; }
  });
  const [localParameters, setLocalParameters] = createSignal<Parameters>(saved?.localParameters ?? {});
  const [localTap, setLocalTap] = createSignal(saved?.localTap ?? "");
  const [localFrom, setLocalFrom] = createSignal(saved?.localFrom ?? "");
  const [localTo, setLocalTo] = createSignal(saved?.localTo ?? "");
  const [activeSearch, setActiveSearch] = createSignal<LocalSelection>();
  const [searchError, setSearchError] = createSignal("");
  const [searching, setSearching] = createSignal(false);
  const [collected, setCollected] = createSignal(0);
  const [availableWindow, setAvailableWindow] = createSignal<TimeWindow>();
  const [viewRevision, setViewRevision] = createSignal(0);
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
  const [tantivyComparison,setTantivyComparison]=createSignal(saved?.query?.engine==='tantivy');
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
    setPreferencesBusy(true);setPreferencesError('');
    try {
      const value=await loadProfile();if(disposed)return;
      setProfile(themeChanged?{...value,appearance:themePreference()}:value);
      if(!themeChanged)setThemePreference(value.appearance);
      if(!saved)setPageSize(value.pageSize);
      if(!value.connections.some(c=>c.enabled&&c.id===activeServer()))setActiveServer(value.connections.find(c=>c.enabled)?.id??'');
    }catch(cause){if(!disposed)setPreferencesError(`Could not load profile: ${cause instanceof Error?cause.message:String(cause)}`);}
    finally{if(!disposed){setProfileReady(true);setPreferencesBusy(false)}}
  }
  onMount(()=>{void loadSavedIndicators();void loadPreferences();});
  async function saveUserProfile(value:UserProfile,tokens?:Record<string,string>){
    const normalized=await saveProfile(value);
    batch(()=>{
    if(tokens){
      const credentials:Record<string,string>={};
      for(const connection of normalized.connections){
        credentials[connection.id]=tokens[connection.id]??'';
      }
      setConnectionTokens(credentials);
    }
    setProfile(normalized);setThemePreference(normalized.appearance);themeChanged=true;setPageSize(normalized.pageSize);
    if(!normalized.connections.some(c=>c.enabled&&c.id===activeServer()))chooseServer(normalized.connections.find(c=>c.enabled)?.id??'');
    });
    await saveTheme(normalized.appearance);
  }
  function openProfile(tab:ProfileTab='preferences'){setProfileTab(tab);navigate('Profile');}
  function chooseServer(id:string){if(id!==activeServer())setActiveServer(id);}

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

  type BootstrapState={loading:boolean;trunk?:TrunkBootstrap;error?:string};
  const [bootstraps,setBootstraps]=createSignal<Record<string,BootstrapState>>({});
  const registrations=createMemo(()=>JSON.stringify(profile().connections));
  createEffect(()=>{
    tapRefresh();if(!profileReady()||managingStorage())return;
    const connections=JSON.parse(registrations()) as ServerConnection[],credentials=connectionTokens(),abort=new AbortController();
    setBootstraps(Object.fromEntries(connections.filter(c=>c.enabled).map(c=>[c.id,{loading:true}])));
    for(const connection of connections.filter(c=>c.enabled)){
      const request=createAbortScope([abort.signal],30000),signal=request.signal;
      void loadTrunk(connection,credentials[connection.id]??'',signal).then(trunk=>{
        if(!abort.signal.aborted)setBootstraps(current=>({...current,[connection.id]:{loading:false,trunk}}));
      }).catch(cause=>{
        if(!abort.signal.aborted)setBootstraps(current=>({...current,[connection.id]:{loading:false,error:cause instanceof Error?cause.message:String(cause)}}));
      }).finally(request.dispose);
    }
    onCleanup(()=>abort.abort());
  });
  createEffect(()=>{
    const state=bootstraps()[activeServer()];
    setTaps(state?.trunk?.catalog.sources??[]);setCollections(state?.trunk?.catalog.collections??[]);
    setTapsLoading(!profileReady()||!!state?.loading);
    setTapError(state?.error??(profileReady()&&!activeRegistration()?'Enable a server in Profile → Connections.':''));
  });
  createEffect(()=>{
    const state=bootstraps()[activeServer()],scope=tap();
    setSchemaLoading(!profileReady()||!!state?.loading);setSchemaError(state?.error??'');
    const trunk=state?.trunk;
    if(!trunk){setQueryFields([]);return;}
    const definitions=new DefinitionChoices(trunk.definitions,trunk.bindings.filter(b=>matchesSource(scope,b.tap)));
    setQueryFields(definitions.fields());
  });

  async function restore(tokenValue: string, restoreWindow = false) {
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
    if(!activeRegistration()){setStatus('idle');return;}
    let last: string | undefined;
    try {
      last = sessionStorage.getItem("snoot:search:collection") ?? undefined;
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
          if(restoreWindow){
            setTap(resourceScope(saved.selection.tap));
            setRange(saved.range);
            setFrom(saved.from);
            setTo(saved.to);
          }

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
            sessionStorage.setItem("snoot:search:collection", saved.id);
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

  let initialRestore=true,previousContext:string|undefined;
  createEffect(() => {
    if(!profileReady())return;
    const context=contextKey();
    const credential = token();
    if(context===previousContext)return;
    const changed=previousContext!==undefined;previousContext=context;
    if(initialRestore&&saved?.query){initialRestore=false;return;}
    const restoreWindow=initialRestore&&!saved;
    initialRestore=false;
    untrack(() => {if(changed){setTap('');setHistoryObject(undefined);setDiagnostic(undefined);setIndicatorField(undefined);}void restore(credential,restoreWindow);});
  });

  let pageScroll!:HTMLDivElement;
  let pageContent!:HTMLDivElement;
  let tabState:TabState;
  const positions:Partial<Record<Page,ScrollPosition>>={...saved?.positions};
  if(saved&&!positions[saved.page])positions[saved.page]={top:saved.scroll,left:0};
  let scrollPending=false;
  let stopScrollRestore=()=>{};
  const saveTab=()=>{if(tabState)writeTab('snoot:workspace',{...tabState,positions,scroll:positions[page()]?.top??0})};
  const captureScroll=()=>{
    const el=pageScroll;
    if(el&&!scrollPending)positions[page()]=capturePosition(el);
    saveTab();
  };
  createEffect(()=>{
    tabState={queryText:queryText(),version:1,page:page(),collapsed:collapsed(),tap:tap(),range:range(),from:from(),to:to(),
      localParameters:localParameters(),localTap:localTap(),localFrom:localFrom(),localTo:localTo(),
      connection:activeServer(),searchView:searchView(),query:pagedQuery(),pageSize:pageSize(),scroll:positions[page()]?.top??0};
    saveTab();
  });
  onMount(()=>{
    history.scrollRestoration='manual';
    let timer:ReturnType<typeof setTimeout>|undefined;
    const scroll=()=>{clearTimeout(timer);timer=setTimeout(captureScroll,150)};
    const flush=()=>{clearTimeout(timer);captureScroll()};
    pageScroll.addEventListener('scroll',scroll,{passive:true});addEventListener('pagehide',flush);
    onCleanup(()=>{clearTimeout(timer);pageScroll.removeEventListener('scroll',scroll);removeEventListener('pagehide',flush)});
  });
  createEffect(()=>{
    const current=page();
    const target=positions[current]??{top:0,left:0};
    stopScrollRestore();scrollPending=true;
    let frame=0;
    const stop=()=>{cancelAnimationFrame(frame);observer.disconnect();clearTimeout(timeout);scrollPending=false;
      removeEventListener('wheel',stop);removeEventListener('touchstart',stop);removeEventListener('keydown',stop)};
    const restore=()=>{cancelAnimationFrame(frame);frame=requestAnimationFrame(()=>{
      const el=pageScroll;if(!el)return;
      if(current==='Search'&&pagedQuery()&&!pagedView().result)return;
      // Lazy pages and restored search rows must render before clamping the position.
      const position=resolvePosition(target,el);
      el.scrollTo(position.left,position.top);
      if(el.scrollHeight-el.clientHeight>=position.top&&Math.abs(el.scrollTop-position.top)<2)stop();
    })};
    const observer=new ResizeObserver(restore);
    observer.observe(pageContent);
    observer.observe(pageScroll);
    const timeout=setTimeout(stop,5000);
    addEventListener('wheel',stop,{passive:true});addEventListener('touchstart',stop,{passive:true});addEventListener('keydown',stop);
    stopScrollRestore=stop;restore();onCleanup(stop);
  });

  function refresh() {
    setViewRevision((value) => value + 1);
    setCollected(working.collected.events.length);
    setAvailableWindow(collectionWindow(working.collected.events));
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
      start = new Date(parseZonedTimestamp(zonedTimestamp(now.getTime(),profile().timeZone).slice(0,10)+"T00:00:00",profile().timeZone));
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

  const hourlyWindow=createMemo(()=>{try{return selection(false)}catch{return undefined}});

  async function submitSearch() {
    if(!activeRegistration()||!bootstraps()[activeServer()]?.trunk){setSearchError('Connect to a server in Profile → Connections before searching.');return;}
    try {
      const ast=parseQuery(queryText(),queryFields());
      for(const [field,value] of Object.entries(localParameters()))if(value)ast.plan.filters.push({field,op:'==',value:{kind:'string',value}});
      const q=selection(false);stop();setError("");setSearchError("");
      setPagedQuery({engine:tantivyComparison()?'tantivy':'local',tap:q.tap,from:q.from,to:q.to!,size:ast.page?.size??pageSize(),after:ast.page?.after,
        ...(ast.plan.filters.length||ast.plan.predicate||ast.plan.search||ast.plan.order!=='asc'?{query:ast.plan}:{})});
    }catch(e){setSearchError(e instanceof Error?e.message:String(e))}
  }

  async function collect(follow = false) {
    if(!activeRegistration()||!bootstraps()[activeServer()]?.trunk){setError('Connect to a server in Profile → Connections before collecting.');return;}
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
        sessionStorage.setItem("snoot:search:collection", collectionId);
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
      undefined,
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
    setSearchView("events");
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
        theme={themePreference()}
        themeBusy={themeBusy()||!profileReady()}
        themeError={themeError()}
        onToggleTheme={() => void chooseTheme(nextTheme(themePreference()))}
        page={page()}
        count={pagedQuery()?pagedView().result?.summary?.count??0:collected()}
        onNavigate={navigate}
        onHelp={() => openModal("help")}
        profileName={profile().name}
        onProfile={() => openProfile()}
        onLogout={props.onLogout}
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
              <Show when={profile().connections.filter(c=>c.enabled).length>1}><label class="control"><span>Server</span><select aria-label="Server" value={activeServer()} onChange={e=>chooseServer(e.currentTarget.value)}><For each={profile().connections.filter(c=>c.enabled)}>{connection=><option value={connection.id}>{connection.name}</option>}</For></select></label></Show>

            <button class="guide-button" aria-label="Quick Guide" onClick={() => openModal("help")}>
              <Icon name="help" size={16} />
              <span>Quick Guide</span>
            </button>
            <div class="theme-selector" role="group" aria-label="Appearance">
              <For each={['light', 'dark', 'auto'] as const}>{choice => <button type="button" aria-pressed={themePreference() === choice} disabled={themeBusy()||!profileReady()} onClick={() => void chooseTheme(choice)}>{choice === 'light' ? 'Light' : choice === 'dark' ? 'Dark' : 'Auto'}</button>}</For>
            </div>
          </div>
        </header>

        <Show when={!managingStorage()}><Show when={contextKey()} keyed>{_connection=><SummarySubscriptions token={token()} sources={taps()} loading={tapsLoading()}/>}</Show></Show>
        <div class="page-scroll" ref={pageScroll}>
        <div class="page-content" ref={pageContent}>
          <Show when={contextKey()} keyed>{_connection=><>
          <Show when={page()==='Profile'||!!bootstraps()[activeServer()]?.trunk} fallback={<section class="card profile-card"><p role="status">{tapError()||'Connecting to the registered server…'}</p><button class="button secondary" onClick={()=>openProfile('connections')}>Open Connections</button><button class="text-button" onClick={()=>setTapRefresh(n=>n+1)}>Retry</button></section>}>
          {/* Preserve the current page and snapshot across navigation. */}
          <div style={{display:page() === "Search"?"contents":"none"}}>
            <form
              ref={collectionCard}
              class="card query-card"
              aria-label="Search Events"
              onSubmit={(event) => {
                event.preventDefault();
                event.currentTarget.querySelector<HTMLTextAreaElement>('#snoot-query')?.blur();
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
                <span class="scope-count">
                  <Show when={pagedQuery() && pagedView().timing}>
                    <SearchPerformance timing={pagedView().timing} matched={pagedView().result?.summary?.count} loaded={pagedView().loaded} />
                  </Show>
                </span>
              </div>
              <Sources value={tap()} catalog={taps()} loading={tapsLoading()} onChange={value=>{setTap(value);setPagedQuery(undefined)}} />
              <Show when={Object.values(localParameters()).some(Boolean)}><div class="query-filter-chips"><For each={Object.entries(localParameters()).filter(([,value])=>!!value)}>{([field,value])=><button type="button" class="text-button" aria-label={`Remove ${field} filter`} onClick={()=>setLocalParameters(current=>{const next={...current};delete next[field];return next})}>{field} == {JSON.stringify(value)} ×</button>}</For></div></Show>
              <QueryEditor value={queryText()} onInput={setQueryText} fields={queryFields()} loading={schemaLoading()} />
              <div class="query-controls">
                <label class="control">
                  <Icon name="clock" size={15} />
                  <span class="sr-only">Time Window</span>
                  <select aria-label="Time Window" value={range()} onChange={event=>{setRange(event.currentTarget.value);setPagedQuery(undefined)}}>
                    <option value="today">Today ({profile().timeZone})</option>
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
                  <Icon name="clock" />{windowLabel(searchWindow().from,searchWindow().to,profile().timeZone)}
                </button>
                <label class="query-comparison"><input type="checkbox" checked={tantivyComparison()} onChange={event=>{setTantivyComparison(event.currentTarget.checked);setPagedQuery(undefined)}}/> Tantivy Comparison</label>
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
                <button class="button primary" type="submit" disabled={searching() || schemaLoading() || !queryValid()}>
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
                  onClick={() => openProfile('connections')}
                >
                  Connections
                  <Icon name="arrow" size={14} />
                </button>
              </div>
            </Show>

            <div class="view-tabs search-view-tabs" role="tablist" aria-label="Search Results" onKeyDown={event => {
              const choices = ['events', 'charts'] as const;
              const next = event.key === 'Home' ? 'events' : event.key === 'End' ? 'charts'
                : ['ArrowLeft', 'ArrowRight'].includes(event.key) ? choices[searchView() === 'events' ? 1 : 0] : undefined;
              if (!next) return;
              event.preventDefault(); setSearchView(next);
              document.getElementById(`search-${next}-tab`)?.focus();
            }}>
              <For each={['events', 'charts'] as const}>{view => <button type="button" role="tab"
                id={`search-${view}-tab`} aria-controls={`search-${view}-panel`} aria-selected={searchView() === view}
                tabIndex={searchView() === view ? 0 : -1} classList={{active: searchView() === view}}
                onClick={() => setSearchView(view)}>{view === 'events' ? 'Events' : 'Charts'}</button>}</For>
            </div>
            <div id="search-events-panel" role="tabpanel" aria-labelledby="search-events-tab" hidden={searchView() !== 'events'}>
            <Show when={bootstraps()[activeServer()]?.trunk&&pagedQuery()} keyed>{query=><PagedResults timeZone={profile().timeZone} query={query} token={token()} pageSize={query.size} active={page()==="Search"&&searchView()==="events"}
              matchers={matchers()} onIndicator={setIndicatorField} indicatorsLoading={indicatorsLoading()}
              collections={collections()} onObjectHistory={openObjectHistory}
              diagnosticSources={taps()} onDiagnostic={setDiagnostic}
              onState={setPagedView} />}</Show>
            <Show when={!pagedQuery()&&bootstraps()[activeServer()]?.trunk}>{trunk=><TimeActivity trunk={trunk()} scope={tap()} from={hourlyWindow()?.from??''} to={hourlyWindow()?.to??''} token={token()} active={page()==='Search'&&searchView()==='events'} onWindow={(a,z)=>{setRange('custom');setFrom(new Date(a).toISOString().replace(/Z$/,''));setTo(new Date(z).toISOString().replace(/Z$/,''));}}/>}</Show>
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
                                : "Snoot through logs with search..."}
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
                    <Events timeZone={profile().timeZone}
                      revision={viewRevision()}
                      events={records()}
                      selected={selected()}
                      select={(event) =>
                        setSelected(selected() === event ? undefined : event)
                      }
                      annotations={annotations()}
                        active={page()==="Search"&&searchView()==="events"}
                      onIndicator={setIndicatorField}
                      indicatorsLoading={indicatorsLoading()}
                      reveal={revealed()}
                      collections={collections()} onObjectHistory={openObjectHistory}
                      diagnosticSources={taps()} onDiagnostic={setDiagnostic}
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
                  onSelect={(_scope, value) => narrow( "service", value)}
                />
                <Breakdown
                  title="Levels"
                  values={summary().levels}
                  total={records().length}
                  levels
                  onSelect={(_scope, value) => narrow( "level", value)}
                />
              </aside>
            </div></Show>
            </div>
            <Show when={page() === "Search" && searchView() === "charts"}>
              <div id="search-charts-panel" role="tabpanel" aria-labelledby="search-charts-tab">
            <Show when={pagedQuery()} fallback={<>
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
                  onSelect={(_scope, value) => narrow( "service", value)}
                />
                <Breakdown
                  title="Levels"
                  values={summary().levels}
                  total={records().length}
                  levels
                  onSelect={(_scope, value) => narrow( "level", value)}
                />
              </aside>
            </div>
            </>}>
              <Show when={pagedView().error}><div class="notice error-notice" role="alert">{pagedView().error}</div></Show>
              <Show when={pagedView().result?.summary} fallback={<p role="status">{pagedView().busy?"Loading search summary…":"No search summary available."}</p>}>
                {s=><div class="dashboard-layout" data-search-charts>
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
              </div>
            </Show>
          </div>

          <Show when={page() === "Collections"}>
            <div class="page-intro"><div><h2>Collections</h2></div></div>
            <Research mode="Object History" token={token()} sources={collections()} initialSource={tap()} pageSize={pageSize()} onIndicator={setIndicatorField} indicatorsLoading={indicatorsLoading()}/>
          </Show>
          <Show when={page() === "Dashboards"}>
            <DashboardWorkspace token={token()} sources={taps()} loading={tapsLoading()} catalogError={tapError()}/>
          </Show>

          <Show when={page()==='Profile'}><Profile profile={profile()} loading={preferencesBusy()} loadError={preferencesError()} onReload={()=>void loadPreferences()} tokens={connectionTokens()} tab={profileTab()} onTab={setProfileTab} onSave={saveUserProfile} statuses={bootstraps()} onStorageClearStart={()=>{stop();disconnectClient();}}/></Show>
          <Show when={page() === "Configs"}><Configurations theme={theme()} token={token()}/></Show>
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
          </Show>
          </>}</Show>
          <footer class="page-footer">
            <span>
              <Icon name="leaf" size={13} /> Small footprint. Clear signals.
            </span>
            <span>snoot · powered by trie-trunk</span>
          </footer>
        </div>
        </div>
      </main>

      <Show when={calendar()} keyed>
        {(scope) => (
          <Calendar timeZone={profile().timeZone}
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
                setPagedQuery(undefined);
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
      <Show when={diagnostic()} keyed>{target=><DiagnosticDialog target={target} token={token()} onClose={()=>setDiagnostic(undefined)}/>}</Show>
      <Settings
        panel={modal()}
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
