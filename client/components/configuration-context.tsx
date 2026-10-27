import {For, Show, createEffect, createMemo, createSignal, onCleanup, type JSX} from 'solid-js';
import type {Theme} from '../services/preferences';
import {loadStorage, type StorageSnapshot} from '../services/quota-preview';
import {configuredLimit, quotaFlow, quotaNodes, requestQuotas, scopeKey, type QuotaPolicy} from '../services/quota-policy';
import {storageNodeName, storageScopeLabel} from '../services/storage-labels';
import {quotaTreeOption} from '../services/quota-tree';
import QuotaChart from './quota-chart';

export default function ConfigurationContext(props: {
  token: string; theme: Theme; scope: string[]; onScope: (path: string[]) => void;
  locked: boolean; policy?: QuotaPolicy; children: JSX.Element;
}) {
  const [snapshot, setSnapshot] = createSignal<StorageSnapshot>();
  const [savedPolicy, setSavedPolicy] = createSignal<QuotaPolicy>({poolBytes: null, scopes: []});
  const [error, setError] = createSignal('');
  const [refresh, setRefresh] = createSignal(0);
  const [width, setWidth] = createSignal(1000);
  const [colors, setColors] = createSignal({text: '#ddd', muted: '#aaa', line: '#444', surface: '#212121'});
  createEffect(() => {
    refresh(); const token = props.token, abort = new AbortController();
    setError('');
    void Promise.all([loadStorage(token, abort.signal), requestQuotas(token, abort.signal)])
      .then(([storage, quotas]) => {
        if (abort.signal.aborted) return;
        setSnapshot(storage); setSavedPolicy(quotas.policy);
        if (!storage.nodes.some(n => scopeKey(n.path) === scopeKey(props.scope))) props.onScope([]);
      }).catch(e => {if (!abort.signal.aborted) setError(String(e.message ?? e));});
    onCleanup(() => abort.abort());
  });
  createEffect(() => {
    props.theme;
    const css = getComputedStyle(document.documentElement);
    setColors({text: css.getPropertyValue('--text').trim(), muted: css.getPropertyValue('--muted').trim(), line: css.getPropertyValue('--line').trim(), surface: css.getPropertyValue('--surface').trim()});
  });
  const nodes = createMemo(() => quotaNodes(snapshot()?.nodes ?? []));
  const policy = () => props.policy ?? savedPolicy();
  // A configured pool can exist before any leaf has a reservation. Keep the
  // real inventory visible using consumption until allocations are available.
  const diagramPolicy = createMemo(() => (quotaFlow(nodes(), policy()).get(scopeKey([])) ?? 0) > 0
    ? policy() : {...policy(), managed: false});
  const flow = createMemo(() => quotaFlow(nodes(), diagramPolicy()));
  const option = createMemo(() => quotaTreeOption(nodes(), policy(), props.scope, width(), colors(), configuredLimit(diagramPolicy(), []) === undefined));
  const select = (path: string[]) => {if (!props.locked) props.onScope(path);};
  return <>
    <section class="configuration-context" aria-label="Configuration Context">
      <nav class="quota-breadcrumb" aria-label="Configuration Scope">
        <button disabled={props.locked} aria-current={!props.scope.length ? 'location' : undefined} onClick={() => select([])}>Trunk</button>
        <For each={props.scope}>{(_, index) => <><span>/</span><button disabled={props.locked} aria-current={index() === props.scope.length - 1 ? 'location' : undefined} onClick={() => select(props.scope.slice(0, index() + 1))}>{storageNodeName(props.scope.slice(0, index() + 1))}</button></>}</For>
      </nav>
      <label class="quota-node-picker">Select Node<select disabled={props.locked || !snapshot()} value={scopeKey(props.scope)} onChange={e => select(JSON.parse(e.currentTarget.value))}>
        <For each={nodes()}>{node => <option value={node.id}>{storageScopeLabel(node.path)}</option>}</For>
      </select></label>
      <Show when={error()}><p class="quota-error" role="alert">{error()} <button class="button secondary" disabled={props.locked} onClick={() => setRefresh(v => v + 1)}>Retry</button></p></Show>
      <Show when={!snapshot() && !error()}><p role="status">Loading Storage Tree…</p></Show>
    </section>
    {props.children}
    <section class="quota-tree" aria-label="Storage Allocation Tree">
      <header class="quota-panel-heading"><div><h3>Storage Allocation</h3><span class="quota-status">{configuredLimit(diagramPolicy(), []) === undefined ? 'Current Consumption' : 'Assigned Quotas'}</span></div></header>
      <div class="quota-tree-levels"><For each={['Trunk', 'Cache', 'Service', 'Resource', 'Branch']}>{tier => <span>{tier}</span>}</For></div>
      <Show when={props.locked}><p class="quota-help">Save or discard changes before selecting another node.</p></Show>
      <Show when={(flow().get(scopeKey([])) ?? 0) > 0} fallback={<div class="quota-empty-branches"><p class="quota-help">No space is allocated yet. Cataloged branches remain available below.</p><For each={nodes().filter(n => n.kind === 'branch')}>{node => <button class="button secondary" disabled={props.locked} onClick={() => select(node.path)}>{storageScopeLabel(node.path)}</button>}</For></div>}>
        <div class="quota-tree-viewport"><QuotaChart height={640} option={option()} label="Storage hierarchy from Trunk through Cache, Service, Resource, and Branch. Select Node offers keyboard navigation." onScope={select} onWidth={setWidth}/></div>
      </Show>
    </section>
  </>;
}
