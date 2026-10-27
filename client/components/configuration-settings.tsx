import {For, Show, createEffect, createMemo, createSignal, onCleanup} from 'solid-js';
import {configurationCategories, requestConfiguration, type ConfigurationCategory, type ConfigurationField, type ConfigurationSnapshot} from '../services/configurations';

export default function ConfigurationSettings(props: {token: string; category: ConfigurationCategory; scope: string[]; onDirty: (dirty: boolean) => void}) {
  const scope = () => props.scope;
  const [snapshot, setSnapshot] = createSignal<ConfigurationSnapshot>();
  const [draft, setDraft] = createSignal<Record<string, unknown>>({});
  const [loading, setLoading] = createSignal(false), [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal(''), [notice, setNotice] = createSignal('');
  const [refresh, setRefresh] = createSignal(0);
  const category = () => configurationCategories.find(c => c.id === props.category)!;
  const fields = () => category().fields.filter(f => !(scope().length === 4 && f.resourceOnly));
  const dirty = createMemo(() => !!snapshot() && JSON.stringify(draft()) !== JSON.stringify(snapshot()!.local));
  createEffect(() => props.onDirty(dirty()));
  onCleanup(() => props.onDirty(false));
  const adopt = (value: ConfigurationSnapshot) => {setSnapshot(value); setDraft({...value.local});};
  createEffect(() => {
    refresh(); const path = scope(), token = props.token, abort = new AbortController();
    setLoading(true); setError(''); setNotice(''); setSnapshot(undefined);
    void requestConfiguration(token, path, abort.signal).then(value => {if (!abort.signal.aborted) adopt(value);})
      .catch(e => {if (!abort.signal.aborted) setError(String(e.message ?? e));})
      .finally(() => {if (!abort.signal.aborted) setLoading(false);});
    onCleanup(() => abort.abort());
  });
  const explicit = (field: ConfigurationField) => Object.hasOwn(draft(), field.key);
  const inherited = (field: ConfigurationField) => {
    const value = snapshot()?.effective[field.key];
    return value === undefined || value === null || value === '' ? field.fallback : value;
  };
  const display = (value: unknown) => value === undefined ? 'Runtime Default' : typeof value === 'boolean' ? value ? 'Enabled' : 'Disabled' : String(value);
  const setField = (field: ConfigurationField, value: unknown) => setDraft(d => ({...d, [field.key]: value}));
  const setOverride = (field: ConfigurationField, enabled: boolean) => setDraft(d => {
    const next = {...d};
    if (enabled) next[field.key] = inherited(field) ?? (field.type === 'boolean' ? false : '');
    else delete next[field.key];
    return next;
  });
  async function save() {
    const current = snapshot(); if (!current || saving()) return;
    const values: Record<string, unknown> = {};
    for (const field of fields()) {
      if (draft()[field.key] === current.local[field.key]) continue;
      const value = draft()[field.key];
      if (value !== undefined && field.type === 'number' && (!Number.isSafeInteger(value) || Number(value) < (field.min ?? 0) || Number(value) > (field.max ?? Number.MAX_SAFE_INTEGER))) {
        setError(`Enter a whole number in range for ${field.label}.`); return;
      }
      values[field.key] = value ?? null;
    }
    setSaving(true); setError('');
    try {
      adopt(await requestConfiguration(props.token, scope(), undefined, {revision: current.revision, category: props.category, values}));
      setNotice('Saved to the configuration file. Restart the server to activate these settings.');
    } catch (e) {setError(e instanceof Error ? e.message : String(e));}
    finally {setSaving(false);}
  }
  return <section class="configuration-settings">
    <header class="quota-panel-heading"><div><h3>{category().label}</h3><p class="quota-help">Settings resolve through Trunk, Cache, Service, Resource, and Branch. Only explicit overrides are written.</p></div><button class="button secondary" disabled={loading() || saving() || dirty()} onClick={() => setRefresh(v => v + 1)}>Reload</button></header>
    <Show when={loading()}><p role="status">Loading Configuration…</p></Show>
    <Show when={error()}><p class="quota-error" role="alert">{error()}</p></Show>
    <Show when={notice()}><p role="status">{notice()}</p></Show>
    <Show when={snapshot()}>{current => <>
      <Show when={props.category !== 'transforms'} fallback={<>
        <p class="quota-help">Transforms are bound to exact definitions rather than inherited. Select a resource to inspect its actual transforms.cfg files. Editing transforms is not available in this view yet.</p>
        <For each={current().transforms}>{transform => <section class="configuration-transform"><h4>Definition <code>{transform.definition}</code></h4><Show when={transform.source} fallback={<p class="quota-help">No transforms configured for this definition.</p>}><pre>{transform.source}</pre></Show></section>}</For>
      </>}>
        <p class="quota-help"><code>{current().file}</code> · {current().exists ? 'Existing File' : 'Created On Save'} · {current().inherit ? 'Inheritance Enabled' : 'Inheritance Disabled In File'}</p>
        <fieldset disabled={saving()} class="quota-fields configuration-field-list">
          <For each={fields()}>{field => <div class="configuration-field">
            <div><label for={`config-${field.key}`}>{field.label}</label><p class="quota-help">{field.help}</p><small>Resolved From Saved Files: {display(inherited(field))}</small></div>
            <div class="configuration-field-control"><label class="quota-managed"><input type="checkbox" checked={explicit(field)} onChange={e => setOverride(field, e.currentTarget.checked)}/>Override Here</label>
              <Show when={explicit(field)} fallback={<span class="quota-help">{current().inherit ? 'Inherited / Default' : 'Default'}</span>}>
                <Show when={field.type === 'boolean'} fallback={<Show when={field.type === 'select'} fallback={<input id={`config-${field.key}`} aria-label={field.label} type={field.type === 'number' ? 'number' : 'text'} min={field.min} max={field.max ?? Number.MAX_SAFE_INTEGER} step="1" value={String(draft()[field.key] ?? '')} onInput={e => setField(field, field.type === 'number' ? e.currentTarget.value === '' ? '' : Number(e.currentTarget.value) : e.currentTarget.value)}/>}>
                  <select id={`config-${field.key}`} aria-label={field.label} value={String(draft()[field.key])} onChange={e => setField(field, e.currentTarget.value)}><For each={field.options}>{option => <option value={option}>{option}</option>}</For></select>
                </Show>}>
                  <select id={`config-${field.key}`} aria-label={field.label} value={String(draft()[field.key])} onChange={e => setField(field, e.currentTarget.value === 'true')}><option value="true">Enabled</option><option value="false">Disabled</option></select>
                </Show>
              </Show>
            </div>
          </div>}</For>
          <Show when={!fields().length}><p class="quota-help">These settings belong at resource scope or above. Select an ancestor to configure them.</p></Show>
        </fieldset>
        <footer class="quota-foot"><span>File settings apply on server restart. Existing packed data is not recompressed.</span><div><button class="button secondary" disabled={!dirty() || saving()} onClick={() => {setDraft({...current().local});setError('');}}>Discard Changes</button><button class="button" disabled={!dirty() || saving()} onClick={() => void save()}>{saving() ? 'Saving…' : 'Save Settings'}</button></div></footer>
      </Show>
    </>}</Show>
  </section>;
}
