import FastDashboard from "./fast-dashboard";
import { collectionStatus, retrySummaries } from "./summary-subscriptions";
import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount, type JSX } from 'solid-js';
import { Icon } from './icon';
import { SubscriptionDialog } from './subscription-dialog';
import {
  dashboardChangedEvent, dashboardPartition, emptyDashboard, loadDashboard, saveDashboardChange, serviceDirectory,
  type DashboardChange, type DashboardState,
} from '../services/dashboard-subscriptions';

const serviceTitle = (name: string) => name ? name[0].toUpperCase() + name.slice(1) : 'All';

export default function DashboardWorkspace(props: {
  token: string; sources: string[]; loading: boolean; catalogError: string;
}) {
  const [state, setState] = createSignal<DashboardState>(emptyDashboard()), [key, setKey] = createSignal('');
  const [ready, setReady] = createSignal(false), [busy, setBusy] = createSignal(false), [error, setError] = createSignal('');
  const [adding, setAdding] = createSignal(false), [mobile, setMobile] = createSignal(matchMedia('(max-width: 760px)').matches);
  let channel: BroadcastChannel | undefined;
  onMount(() => {
    const media = matchMedia('(max-width: 760px)'), change = () => setMobile(media.matches);
    media.addEventListener('change', change); onCleanup(() => media.removeEventListener('change', change));
  });
  const catalog = createMemo(() => serviceDirectory(props.sources));
  const subscribed = createMemo(() => state().subscriptions.map(s => s.service));
  const services = createMemo(() => [...subscribed()].sort());
  const visible = createMemo(() => state().subscriptions.filter(s => !state().service || s.service === state().service));
  const available = (id: string) => catalog().some(r => r.id === id);
  function accept(next: DashboardState) { setState(old => next.revision >= old.revision ? next : old); }
  createEffect(() => {
    const token = props.token;
    let disposed = false, localChannel: BroadcastChannel | undefined;
    setKey(''); setReady(false); setBusy(false); setError(''); setAdding(false); setState(emptyDashboard());
    void (async () => {
      try {
        const partition = await dashboardPartition(token), saved = await loadDashboard(partition);
        if (disposed) return;
        setKey(partition); setState(saved); setReady(true);
        if (typeof BroadcastChannel !== 'undefined') {
          localChannel = new BroadcastChannel(partition); channel = localChannel;
          localChannel.onmessage = () => {
            void loadDashboard(partition).then(next => { if (!disposed) accept(next); }, cause => { if (!disposed) setError(String(cause)); });
          };
        }
      } catch (cause) { if (!disposed) setError(cause instanceof Error ? cause.message : String(cause)); }
    })();
    onCleanup(() => { disposed = true; localChannel?.close(); if (channel === localChannel) channel = undefined; });
  });
  async function update(change: DashboardChange): Promise<void> {
    const partition = key();
    if (!ready() || busy() || !partition) throw Error('Dashboard storage is not ready.');
    setBusy(true); setError('');
    try {
      const next = await saveDashboardChange(partition, change);
      if (key() === partition) { accept(next); channel?.postMessage({ changed: true }); dispatchEvent(new CustomEvent(dashboardChangedEvent)); }
    } catch (cause) {
      if (key() === partition) setError(cause instanceof Error ? cause.message : String(cause));
      throw cause;
    } finally { if (key() === partition) setBusy(false); }
  }
  const act = (change: DashboardChange) => { void update(change).catch(() => {}); };
  const closeDrawer = () => act({ kind: 'drawer', open: false });
  const select = (service: string) => act({ kind: 'select', service, closeDrawer: mobile() });
  const Directory = () => <>
    <div class="seed-directory-heading"><h3 id="seed-directory-title">Data Seeds</h3><button class="icon-button" aria-label="Close Data Seeds" disabled={busy()} onClick={closeDrawer}><Icon name="close" /></button></div>
    <nav aria-label="Subscribed Services" class="seed-services">
      <button class="seed-service" aria-current={!state().service ? 'true' : undefined} disabled={busy()} onClick={() => select('')}><span>All</span><span>{subscribed().length}</span></button>
      <For each={services()}>{service => <div class="seed-service-group">
        <button class="seed-service" aria-current={state().service === service ? 'true' : undefined} disabled={busy()} onClick={() => select(service)}><span>{serviceTitle(service.split('/')[1])}</span><span>{service.split('/')[0]}</span></button>
      </div>}</For>
      <Show when={!services().length}><p class="seed-directory-empty">Subscribed services will appear here.</p></Show>
    </nav>
    <button class="button secondary seed-add" disabled={busy() || !ready()} onClick={() => setAdding(true)}>Plant Seed</button>
  </>;
  return <section class="dashboard-workspace" aria-label="Dashboard Subscriptions">
    <div class="page-intro"><div><h2>Dashboards</h2><p>Follow the Data Seeds that matter to you.</p></div><button class="button secondary" aria-expanded={state().drawerOpen} aria-controls="seed-directory" disabled={!ready() || busy()} onClick={() => act({ kind: 'drawer', open: !state().drawerOpen })}>Data Seeds <span class="seed-count">{subscribed().length}</span></button></div>
    <Show when={error()}><div class="notice error-notice" role="alert">{error()}</div></Show>
    <Show when={props.catalogError}><div class="notice error-notice" role="alert">{props.catalogError}</div></Show>
    <Show when={!ready() && !error()}><p role="status">Loading Subscriptions…</p></Show>
    <div class="dashboard-subscription-layout" classList={{ 'directory-open': state().drawerOpen && !mobile() }}>
      <div class="dashboard-subscription-content">
        <div class="subscription-scope"><h3>{serviceTitle(state().service.split('/')[1] ?? '')}</h3><span>{visible().length} {visible().length === 1 ? 'Service' : 'Services'}</span></div>
        <Show when={visible().length} fallback={<div class="card subscription-empty"><Icon name="activity" size={32}/><h3>Plant Your First Seed</h3><p>Subscribe to a service to follow its Data Seeds.</p><button class="button primary" disabled={!ready() || busy()} onClick={() => setAdding(true)}>Plant Seed</button></div>}>
          <FastDashboard token={props.token} services={visible().map(s=>s.service)} revision={visible().map(s=>`${s.service}:${collectionStatus()[s.service]?.state==='ready'}`).join(',')}/>
          <details class="dashboard-subscription-details"><summary>Subscriptions</summary>
          <div class="subscription-cards"><For each={visible()}>{subscription => {
            const parts = subscription.service.split('/');
            const resources = () => catalog().find(s => s.id === subscription.service)?.resources ?? [];
            return <article class="card subscription-card"><div><span class="subscription-path">{parts[0]}</span><h4>{serviceTitle(parts[1])}</h4><p>{props.loading ? 'Checking availability…' : available(subscription.service) ? `${resources().length} ${resources().length === 1 ? 'Resource' : 'Resources'} · Subscribed` : 'Unavailable in Current Catalog'}</p><Show when={collectionStatus()[subscription.service]}>{status => <div class="subscription-sync" role="status">
              <Show when={status().state === 'collecting'}><span>Collecting Summaries</span><progress max={status().total || 1} value={status().completed} aria-label="Summary Collection Progress"/><span>{status().total ? Math.round(100 * status().completed / status().total) : 0}% · Year to Date</span></Show>
              <Show when={status().state === 'ready'}><span>Summaries Up to Date</span></Show>
              <Show when={status().state === 'error'}><span class="subscription-error">{status().error}</span><button type="button" class="text-button" onClick={() => retrySummaries(subscription.service)}>Retry Collection</button></Show>
            </div>}</Show><Show when={resources().length}><ul class="subscription-resources" aria-label={`${serviceTitle(parts[1])} Resources`}><For each={resources()}>{resource => <li>{resource.resource}</li>}</For></ul></Show></div><button class="icon-button" disabled={busy()} aria-label={`Remove Subscription ${subscription.service}`} onClick={() => act({ kind: 'remove', service: subscription.service })}><Icon name="close" /></button></article>;
          }}</For></div>
          <p class="subscription-collection-status">Year-to-date summaries collect first. Older history loads on demand.</p></details>
        </Show>
      </div>
      <Show when={ready() && state().drawerOpen && !(mobile() && adding())}>
        <Show when={mobile()} fallback={<aside id="seed-directory" class="seed-directory" aria-labelledby="seed-directory-title"><Directory /></aside>}>
          <MobileDirectory onClose={closeDrawer} busy={busy()}><Directory /></MobileDirectory>
        </Show>
      </Show>
    </div>
    <Show when={adding()}><SubscriptionDialog services={catalog()} subscribed={subscribed()} loading={props.loading} catalogError={props.catalogError} onClose={() => setAdding(false)} onSave={async (add, remove) => { await update({ kind: 'subscriptions', add, remove }); }} /></Show>
  </section>;
}

function MobileDirectory(props: { children: JSX.Element; busy: boolean; onClose: () => void }) {
  let dialog!: HTMLDialogElement;
  onMount(() => dialog.showModal()); onCleanup(() => dialog.close());
  return <dialog ref={dialog} id="seed-directory" class="seed-directory seed-directory-mobile" aria-labelledby="seed-directory-title" onCancel={e => { e.preventDefault(); if (!props.busy) props.onClose(); }} onClick={e => { if (e.target === dialog && !props.busy) props.onClose(); }}>{props.children}</dialog>;
}
