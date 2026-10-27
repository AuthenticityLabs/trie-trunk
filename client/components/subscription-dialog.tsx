import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import type { SeedService } from '../services/dashboard-subscriptions';
import { Icon } from './icon';

export function SubscriptionDialog(props: {
  services: SeedService[]; subscribed: string[]; loading: boolean; catalogError: string;
  onSave: (add: string[], remove: string[]) => Promise<void>; onClose: () => void;
}) {
  let dialog!: HTMLDialogElement;
  const initial = [...props.subscribed];
  const [cache, setCache] = createSignal(''), [query, setQuery] = createSignal('');
  const [selected, setSelected] = createSignal<string[]>(initial), [saving, setSaving] = createSignal(false), [error, setError] = createSignal('');
  const caches = createMemo(() => [...new Set(props.services.map(r => r.cache))].sort());
  const choices = createMemo(() => props.services.filter(r => !selected().includes(r.id) && (!cache() || r.cache === cache()) && r.id.toLowerCase().includes(query().trim().toLowerCase())));
  const added = () => selected().filter(id => !initial.includes(id));
  const removed = () => initial.filter(id => !selected().includes(id));
  const changed = () => added().length + removed().length > 0;
  onMount(() => dialog.showModal()); onCleanup(() => dialog.close());
  const close = () => { if (!saving()) props.onClose(); };
  async function save(event: SubmitEvent) {
    event.preventDefault(); setSaving(true); setError('');
    try {
      const add = added();
      if (add.some(id => !props.services.some(r => r.id === id))) throw Error('A selected service is no longer available.');
      await props.onSave(add, removed()); props.onClose();
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setSaving(false); }
  }
  const Service = (item: {id: string}) => {
    const entry = () => props.services.find(s => s.id === item.id);
    const name = item.id.split('/')[1];
    return <span><strong>{name[0].toUpperCase() + name.slice(1)}</strong><span>{item.id.split('/')[0]}<Show when={entry()}>{r => <> · {r().resources.length} {r().resources.length === 1 ? 'Resource' : 'Resources'}</>}</Show></span></span>;
  };
  return <dialog ref={dialog} class="modal subscription-modal" aria-labelledby="subscription-title" onCancel={e => { e.preventDefault(); close(); }} onClick={e => { if (e.target === dialog) close(); }}>
    <form class="modal-body" onSubmit={save}>
      <div class="modal-heading"><h2 id="subscription-title">Data Seeds</h2><button type="button" class="icon-button" aria-label="Close Data Seeds Dialog" disabled={saving()} onClick={close}><Icon name="close" /></button></div>
      <p>Seed your dashboards with data for ongoing telemetry and analytics.</p>
      <fieldset disabled={saving() || props.loading} class="subscription-fields">
        <div class="subscription-filters">
          <label>Cache<select value={cache()} onChange={e => setCache(e.currentTarget.value)}><option value="">All Caches</option><For each={caches()}>{name => <option value={name}>{name}</option>}</For></select></label>
          <label>Find Services<input type="search" value={query()} onInput={e => setQuery(e.currentTarget.value)} placeholder="Find a service…" /></label>
        </div>
        <div class="subscription-service-list" role="region" aria-label="Service Selection" tabIndex={0}>
        <section class="subscription-selected" aria-labelledby="selected-services-title">
          <h3 id="selected-services-title">Selected Services <span>{selected().length}</span></h3>
          <div class="subscription-options" role="group" aria-label="Selected Services">
            <For each={selected()}>{id => <div class="subscription-option"><Service id={id}/><button type="button" class="subscription-move" aria-label={`Remove ${id}`} onClick={() => setSelected(old => old.filter(v => v !== id))}>Remove</button></div>}</For>
            <Show when={!selected().length}><p>No services selected.</p></Show>
          </div>
        </section>
        <h3>Available Services</h3>
        <div class="subscription-options" role="group" aria-label="Available Services">
          <For each={choices()}>{r => <div class="subscription-option"><Service id={r.id}/><button type="button" class="subscription-move subscription-add" aria-label={`Add ${r.id}`} onClick={() => setSelected(old => [r.id, ...old])}>Add</button></div>}</For>
          <Show when={!choices().length}><p role="status">{props.loading ? 'Loading Services…' : 'No more matching services.'}</p></Show>
        </div>
        </div>
      </fieldset>
      <Show when={error() || props.catalogError}><p class="subscription-error" role="alert">{error() || props.catalogError}</p></Show>
      <div class="subscription-modal-actions"><button type="button" class="button secondary" disabled={saving()} onClick={close}>Cancel</button><button type="submit" class="button primary" disabled={saving() || props.loading || !!props.catalogError || !changed()}>{saving() ? 'Saving…' : 'Seed'}</button></div>
    </form>
  </dialog>;
}
