import {quotaRings} from '../../shared/quota-rings.mjs';
import {For, Show, createEffect, createMemo, createSignal, onCleanup} from 'solid-js';
import type {EChartsCoreOption} from 'echarts/core';
import type {Theme} from '../services/preferences';
import {loadStorage, type StorageSnapshot, GB, previewQuota, estimateRetainedHistory, durationLabel, retentionEstimateLabel, retentionBasisLabel, storageLabel} from '../services/quota-preview';
import {requestQuotas, assignedBytes, configuredLimit, isManaged, quotaBounds, quotaNodes, scopeKey, scopeSetting, updateScope, type QuotaPolicy, type QuotaSettings, type Recovery} from '../services/quota-policy';
import QuotaChart from './quota-chart';
import RetentionSignal from './retention-signal';

const tiers = ['Trunk', 'Cache', 'Service', 'Resource', 'Branch'];
const emptyPolicy: QuotaPolicy = {poolBytes: null, scopes: []};
const gbValue = (bytes: number) => Number((bytes / GB).toPrecision(7));

export default function CacheQuotas(props: {theme: Theme; token: string; scope: string[]; onPolicy: (policy: QuotaPolicy) => void; onDirty?: (dirty: boolean) => void}) {
  const scope = () => props.scope;
  const [snapshot, setSnapshot] = createSignal<StorageSnapshot>();
  const [saved, setSaved] = createSignal<QuotaSettings>();
  const [policy, setPolicy] = createSignal<QuotaPolicy>(emptyPolicy);
  const [error, setError] = createSignal('');
  const [notice, setNotice] = createSignal('');
  const [loading, setLoading] = createSignal(true);
  const [saving, setSaving] = createSignal(false);
  const [refresh, setRefresh] = createSignal(0);
  const [stage, setStage] = createSignal<'Current' | 'At Capacity' | 'After Recovery'>('Current');
  const [colors, setColors] = createSignal({text: '#ddd', muted: '#aaa', line: '#444', surface: '#212121'});
  createEffect(() => {
    refresh(); const token = props.token, abort = new AbortController();
    setLoading(true); setError(''); setNotice('');
    void Promise.all([loadStorage(token, abort.signal), requestQuotas(token, abort.signal)])
      .then(([storage, settings]) => {if (!abort.signal.aborted) {setSnapshot(storage); setSaved(settings); setPolicy(settings.policy);}})
      .catch(e => {if (!abort.signal.aborted) setError(String(e.message ?? e));})
      .finally(() => {if (!abort.signal.aborted) setLoading(false);});
    onCleanup(() => abort.abort());
  });
  createEffect(() => {
    props.theme;
    const css = getComputedStyle(document.documentElement);
    setColors({text: css.getPropertyValue('--text').trim(), muted: css.getPropertyValue('--muted').trim(), line: css.getPropertyValue('--line').trim(), surface: css.getPropertyValue('--surface').trim()});
  });
  const samples = () => snapshot()?.samples ?? [];
  const nodes = createMemo(() => quotaNodes(snapshot()?.nodes ?? []));
  const selected = createMemo(() => nodes().find(n => n.id === scopeKey(scope()))!);
  const leaf = () => selected()?.kind === 'branch';
  const managed = createMemo(() => isManaged(policy(), scope()));
  const dirty = createMemo(() => !!saved() && JSON.stringify(saved()!.policy) !== JSON.stringify(policy()));
  createEffect(() => props.onDirty?.(dirty()));
  onCleanup(() => props.onDirty?.(false));
  const bounds = createMemo(() => quotaBounds(policy(), scope(), saved()?.disk.maximumBytes ?? 0));
  const configured = createMemo(() => configuredLimit(policy(), scope()));
  const budget = createMemo(() => configured() ?? bounds().maximum);
  const assigned = createMemo(() => assignedBytes(policy(), scope()));
  const recovery = createMemo<Recovery>(() => scopeSetting(policy(), scope())?.recovery ?? {unit: 'percent', value: 25});
  const recoveryBytes = createMemo(() => Math.min(budget(), recovery().unit === 'percent' ? budget() * recovery().value / 100 : recovery().value));
  const evaluation = createMemo(() => previewQuota(samples(), scope(), budget(), recoveryBytes()));
  const retained = createMemo(() => estimateRetainedHistory(evaluation().hours, budget(), recoveryBytes()));
  const estimate = createMemo(() => retentionEstimateLabel(retained()));
  const capacityHistory = createMemo(() => estimateRetainedHistory(evaluation().hours, budget(), 0));
  const shownUsage = () => !managed() || stage() === 'Current' ? selected()?.bytes ?? 0 : stage() === 'At Capacity' ? budget() : budget() - recoveryBytes();
  const edit = (next: QuotaPolicy) => {setPolicy(next); setNotice('');};
  const setManaged = (enabled: boolean) => {
    edit(scope().length ? updateScope(policy(), scope(), {managed: enabled}) : {...policy(), managed: enabled});
    setStage('Current'); setError('');
  };
  const waitingForPool = () => scope().length > 0 && isManaged(policy(), []) && policy().poolBytes === null;
  const setLimit = (bytes?: number) => {
    if (bytes !== undefined && (!Number.isFinite(bytes) || bytes < bounds().minimum || bytes > bounds().maximum)) {
      setError(`Choose a value between ${storageLabel(bounds().minimum)} and ${storageLabel(bounds().maximum)}.`); return;
    }
    setError('');
    edit(scope().length ? updateScope(policy(), scope(), {limitBytes: bytes}) : {...policy(), poolBytes: bytes ?? null});
  };
  const setRecovery = (value: Recovery) => edit(updateScope(policy(), scope(), {recovery: value}));
  createEffect(() => {props.scope; setStage('Current');});
  createEffect(() => {if (saved()) props.onPolicy(policy());});
  const save = async () => {
    if (!saved() || saving()) return;
    setSaving(true); setError('');
    try {
      const next = await requestQuotas(props.token, undefined, saved(), policy());
      setSaved(next); setPolicy(next.policy); setNotice('Quota settings saved. Automatic recovery is not enabled.');
    } catch (e) {setError(e instanceof Error ? e.message : String(e));}
    finally {setSaving(false);}
  };

  const ringOption = createMemo<EChartsCoreOption>(() => {
    const c = colors();
    const paths = quotaRings(budget(), shownUsage(), Math.max(budget(), bounds().maximum, 1));
    return {series: [{type: 'custom', coordinateSystem: 'none', silent: true, data: [0], renderItem: (_: unknown, api: {getWidth: () => number; getHeight: () => number}) => {
      const size = Math.min(api.getWidth(), api.getHeight());
      return {type: 'group', x: (api.getWidth() - size) / 2, y: (api.getHeight() - size) / 2, scaleX: size / 320, scaleY: size / 320, children: paths.map(p => ({type: 'path', shape: {pathData: p.d}, style: {fill: 'none', stroke: p.role === 'filledRing' && managed() ? '#718f91' : c.text, opacity: p.role === 'filledRing' ? managed() ? 1 : .35 : p.role === 'bark' ? .25 : .10, lineWidth: p.role === 'filledRing' ? 4 : 1}}))};
    }}]};
  });

  return <section class="quota-view">
    <header class="quota-heading"><div><span class="eyebrow">STORAGE & RETENTION</span><h3>Quotas</h3></div><button class="button secondary" disabled={loading() || saving() || dirty()} onClick={() => setRefresh(v => v + 1)}>Reload</button></header>
    <Show when={loading()}><p role="status">Loading Storage Settings…</p></Show>
    <Show when={error()}><p class="quota-error" role="alert">{error()}</p></Show>
    <Show when={notice()}><p role="status">{notice()}</p></Show>
    <Show when={saved() && snapshot()}>
      <section classList={{'quota-tuning': true, 'quota-unmanaged': !managed()}} aria-label="Quota Settings">
        <header class="quota-panel-heading"><div><span class="eyebrow">{tiers[scope().length]}</span><h3>{selected()?.name ?? 'Trunk'}</h3></div><label class="quota-managed"><input type="checkbox" checked={managed()} disabled={loading() || saving()} onChange={e => setManaged(e.currentTarget.checked)}/>Managed</label></header>
        <fieldset disabled={loading() || saving()} class="quota-fields">
          <div class="quota-tuning-body quota-has-rings">
            <div class="quota-controls">
              <Show when={waitingForPool() && managed()}><p class="quota-status">Set the Trunk pool first.</p></Show>
              <Show when={managed()}>
              <div class="quota-budget">
                <div class="quota-budget-label"><label for="quota-budget">{!scope().length ? 'Trunk Pool' : leaf() ? 'Leaf Quota' : 'Pool Limit'}</label><label class="quota-size-input"><input aria-label="Quota In GB" type="number" min={bounds().minimum / GB} max={bounds().maximum / GB} step="any" disabled={waitingForPool()} value={gbValue(budget())} onChange={e => setLimit(Math.round(Number(e.currentTarget.value) * GB))}/><span>GB</span></label></div>
                <input id="quota-budget" type="range" min={bounds().minimum} max={Math.max(bounds().minimum, bounds().maximum)} step="1" disabled={waitingForPool()} value={budget()} aria-valuetext={storageLabel(budget())} onInput={e => setLimit(Number(e.currentTarget.value))}/>
                <div class="quota-scale"><span>{storageLabel(bounds().minimum)}</span><span>{storageLabel(bounds().maximum)} Available Limit</span></div>
                <Show when={!scope().length && policy().poolBytes === null}><button class="button secondary" onClick={() => setLimit(bounds().maximum)}>Set Trunk Pool</button></Show>
              </div>
              <div class="quota-history-result">
                <span>Estimated Capacity</span>
                <output for="quota-budget">{durationLabel(evaluation().total > 0 ? capacityHistory().range?.atCapacity ?? null : null)}</output>
                <Show when={capacityHistory().status === 'out-of-range'}><RetentionSignal basis={retentionBasisLabel(evaluation().hours)}/></Show>
              </div>
              </Show>
              <dl class="quota-totals"><div><dt>Consumed</dt><dd>{storageLabel(selected()?.bytes ?? 0)}</dd></div><div><dt>Assigned To Leaves</dt><dd>{storageLabel(assigned())}</dd></div><div><dt>{!managed() ? 'Inherited Capacity' : leaf() ? 'Quota Remaining' : 'Unassigned'}</dt><dd>{!managed() ? storageLabel(budget()) : waitingForPool() || !scope().length && policy().poolBytes === null ? 'Not Set' : storageLabel(Math.max(0, budget() - (leaf() ? selected()?.bytes ?? 0 : assigned())))}</dd></div></dl>
            </div>
            <div class="quota-consumption"><h4>Consumption <Show when={!managed()}><span class="quota-inactive-label">Unmanaged</span></Show></h4><QuotaChart height={300} option={ringOption()} label={`${managed() ? 'Active' : 'Inactive'} consumption rings for ${selected()?.name ?? 'Trunk'}. Twenty-eight stationary rings; consumption fills inward.`}/><Show when={managed()}><div class="quota-stage" role="group" aria-label="Consumption State"><For each={leaf() ? ['Current', 'At Capacity', 'After Recovery'] as const : ['Current', 'At Capacity'] as const}>{s => <button aria-pressed={stage() === s} onClick={() => setStage(s)}>{s}</button>}</For></div></Show><p class="quota-ring-values">{managed() ? 'Quota' : 'Inherited Capacity'}: {storageLabel(budget())} · Consumed: {storageLabel(shownUsage())}</p></div>
          </div>
          <Show when={leaf() && managed()}>
            <section class="quota-recovery" aria-label="Space Recovery">
              <div class="quota-budget-label"><label for="quota-recovery">Recover When Full</label><div class="quota-units" role="group" aria-label="Recovery Unit"><For each={['percent', 'bytes'] as const}>{unit => <button aria-pressed={recovery().unit === unit} onClick={() => setRecovery({unit, value: unit === 'percent' ? budget() ? recoveryBytes() / budget() * 100 : 0 : Math.round(recoveryBytes())})}>{unit === 'percent' ? '%' : 'GB'}</button>}</For></div></div>
              <output class="quota-recovery-amount" for="quota-recovery"><span>{recovery().unit === 'percent' ? `${(budget() ? recoveryBytes() / budget() * 100 : 0).toLocaleString(undefined, {maximumFractionDigits: 1})}%` : storageLabel(recoveryBytes())}</span><span>{recovery().unit === 'percent' ? storageLabel(recoveryBytes()) : `${(budget() ? recoveryBytes() / budget() * 100 : 0).toLocaleString(undefined, {maximumFractionDigits: 1})}%`}</span></output>
              <input id="quota-recovery" type="range" min="0" max={recovery().unit === 'percent' ? 100 : budget()} step={recovery().unit === 'percent' ? .1 : 1} value={recovery().value} aria-valuetext={`${storageLabel(recoveryBytes())}, ${retained().status === 'insufficient' ? estimate() : `${estimate()} retained history${retained().status === 'out-of-range' ? ', beyond forecast range' : ''}`}`} onInput={e => setRecovery({unit: recovery().unit, value: Number(e.currentTarget.value)})}/>
              <div class="quota-retained-history"><h4>Estimated Retained History</h4><div class="quota-retained-result"><output classList={{'quota-retained-value': true, 'quota-estimate-unavailable': retained().status === 'insufficient'}} aria-label={retained().status === 'insufficient' ? estimate() : `${estimate()}, after recovery to capacity`}>{estimate()}</output><Show when={retained().status === 'out-of-range'}><RetentionSignal basis={retentionBasisLabel(evaluation().hours)}/></Show></div></div>
            </section>
          </Show>
        </fieldset>
        <footer class="quota-foot"><div class="quota-save-status"><span>{dirty() ? 'Unsaved Changes' : 'Saved'}</span><span class="quota-status-badge">Automatic Recovery Off</span></div><div><button class="button secondary" disabled={!dirty() || saving()} onClick={() => {setPolicy(saved()!.policy); setError(''); setNotice('');}}>Discard Changes</button><button class="button" disabled={!dirty() || saving()} onClick={() => void save()}>{saving() ? 'Saving…' : 'Save Settings'}</button></div></footer>
      </section>
    </Show>
  </section>;
}
