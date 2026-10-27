import { createEffect, createSignal, onCleanup } from 'solid-js';
import { dashboardChangedEvent, dashboardPartition, loadDashboard, serviceDirectory, type DashboardState } from '../services/dashboard-subscriptions';
import { primeSummaries } from '../services/pil/client';
import type { SummaryProgress } from '../services/summary-replication';

export type CollectionStatus = SummaryProgress & { state: 'collecting' | 'ready' | 'error'; error?: string };
const [collectionStatus, setCollectionStatus] = createSignal<Record<string, CollectionStatus>>({});
export { collectionStatus };
const retryEvent = 'snoot-summary-retry';
export const retrySummaries = (service: string) => dispatchEvent(new CustomEvent(retryEvent, { detail: service }));

/** Lives with the workspace, so changing menu pages does not stop collection. */
export function SummarySubscriptions(props: {token: string; sources: string[]; loading: boolean}) {
  const [saved, setSaved] = createSignal<DashboardState>();
  let sync = (_refresh = false) => {};
  createEffect(() => {
    const token = props.token;
    let disposed = false, channel: BroadcastChannel | undefined, localChanged = () => {};
    const jobs = new Map<string, {controller: AbortController; catalog: string; complete: boolean; running: boolean}>();
    setSaved(undefined); setCollectionStatus({});
    const status = (service: string, value: CollectionStatus) => {
      if (!disposed) setCollectionStatus(old => ({...old, [service]: value}));
    };
    sync = (refresh = false) => {
      if (disposed || props.loading || !saved()) return;
      const wanted = new Set(saved()!.subscriptions.map(s => s.service));
      for (const [service, job] of jobs) if (!wanted.has(service)) {
        job.controller.abort(); jobs.delete(service);
        setCollectionStatus(old => { const next = {...old}; delete next[service]; return next; });
      }
      for (const service of serviceDirectory(props.sources)) {
        if (!wanted.has(service.id)) continue;
        const catalog = service.resources.flatMap(r => r.sources).join(',');
        const previous = jobs.get(service.id);
        if (previous?.catalog === catalog && (previous.running || !refresh)) continue;
        previous?.controller.abort();
        const recent = !!previous?.complete && previous.catalog === catalog;
        const job = {controller: new AbortController(), catalog, complete: recent, running: true};
        jobs.set(service.id, job);
        status(service.id, {state:'collecting', completed:0, total:0, packets:0});
        void primeSummaries(service.id, recent, token, job.controller.signal, progress => {
          if (jobs.get(service.id) === job) status(service.id, {...progress, state:'collecting'});
        }).then(() => {
          if (jobs.get(service.id) !== job || disposed) return;
          job.complete = true;
          setCollectionStatus(old => ({...old, [service.id]:{...old[service.id],state:'ready'}}));
        }).catch(error => {
          if (!job.controller.signal.aborted) status(service.id, {state:'error',completed:0,total:0,packets:0,error:error instanceof Error?error.message:String(error)});
        }).finally(() => {job.running = false;});
      }
    };
    const refresh = () => sync(true);
    const retry = (event: Event) => {
      const service = (event as CustomEvent<string>).detail, job = jobs.get(service);
      job?.controller.abort(); jobs.delete(service); sync();
    };
    const timer = setInterval(refresh, 5 * 60_000);
    addEventListener(retryEvent, retry);
    void dashboardPartition(token).then(async key => {
      const reload = async () => {
        try {
          const next = await loadDashboard(key);
          if (!disposed) setSaved(old => !old || next.revision >= old.revision ? next : old);
        } catch { /* The subscription editor reports persistence failures. */ }
      };
      if (disposed) return;
      if (typeof BroadcastChannel !== 'undefined') {
        channel = new BroadcastChannel(key); channel.onmessage = () => void reload();
      }
      localChanged = () => void reload();
      addEventListener(dashboardChangedEvent, localChanged);
      await reload();
    }).catch(() => {});
    onCleanup(() => {
      disposed = true; removeEventListener(dashboardChangedEvent, localChanged); channel?.close(); clearInterval(timer); removeEventListener(retryEvent, retry);
      for (const job of jobs.values()) job.controller.abort();
    });
  });
  createEffect(() => { props.sources; props.loading; saved(); sync(); });
  return null;
}
