import {For, Show, createSignal, lazy} from 'solid-js';
import type {Theme} from '../services/preferences';
import {configurationCategories, type ConfigurationCategory} from '../services/configurations';

import ConfigurationContext from './configuration-context';
import type {QuotaPolicy} from '../services/quota-policy';

const Quotas = lazy(() => import('./cache-quotas'));
const Settings = lazy(() => import('./configuration-settings'));
const tabs = [{id: 'quotas' as const, label: 'Quotas'}, ...configurationCategories];

export default function Configurations(props: {theme: Theme; token: string}) {
  const [selected, setSelected] = createSignal<'quotas' | ConfigurationCategory>('quotas');
  const [scope, setScope] = createSignal<string[]>([]);
  const [policy, setPolicy] = createSignal<QuotaPolicy>();
  const [dirty, setDirty] = createSignal(false);
  const select = (id: 'quotas' | ConfigurationCategory) => {if (!dirty()) setSelected(id);};
  return <section class="configurations-view" aria-label="Configurations">
    <header class="page-intro"><div><h2>Configurations</h2></div></header>
    <ConfigurationContext token={props.token} theme={props.theme} scope={scope()} onScope={setScope} locked={dirty()} policy={policy()}>
    <div class="view-tabs" role="tablist" aria-label="Configuration Categories" onKeyDown={e => {
      if (dirty() || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
      e.preventDefault();
      const index = tabs.findIndex(t => t.id === selected());
      const next = e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : (index + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
      select(tabs[next].id); document.getElementById(`configs-${tabs[next].id}-tab`)?.focus();
    }}>
      <For each={tabs}>{tab => <button type="button" role="tab" id={`configs-${tab.id}-tab`} aria-controls={`configs-${tab.id}-panel`} aria-selected={selected() === tab.id} tabIndex={selected() === tab.id ? 0 : -1} disabled={dirty() && selected() !== tab.id} classList={{active: selected() === tab.id}} onClick={() => select(tab.id)}>{tab.label}</button>}</For>
    </div>
    <Show when={dirty()}><p class="quota-help">Save or discard changes before switching configuration tabs.</p></Show>
    <div id={`configs-${selected()}-panel`} role="tabpanel" aria-labelledby={`configs-${selected()}-tab`}>
      <Show when={selected() === 'quotas'} fallback={<Show when={selected() !== 'quotas' ? selected() as ConfigurationCategory : undefined} keyed>{category => <Settings category={category} token={props.token} scope={scope()} onDirty={setDirty}/>}</Show>}>
        <Quotas theme={props.theme} token={props.token} scope={scope()} onPolicy={setPolicy} onDirty={setDirty}/>
      </Show>
    </div>
    </ConfigurationContext>
  </section>;
}
