import { Show, createSignal, onMount } from 'solid-js';

type Connection = {
  transport: string;
  preference: 'auto' | 'socket' | 'http';
  endpoint: string;
  socket: string;
  pending: boolean;
};

export function DesktopConnection() {
  const [connection, setConnection] = createSignal<Connection>();
  const [preference, setPreference] = createSignal('auto');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  async function update(save = false) {
    setBusy(true); setError('');
    try {
      const response = await fetch('/__desktop/connection', save ? {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ transport: preference() }),
      } : { cache: 'no-store' });
      if (!response.ok) throw Error((await response.text()).trim());
      const value: Connection = await response.json();
      setConnection(value); setPreference(value.preference);
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setBusy(false); }
  }
  onMount(() => void update());
  return <section class="desktop-connection" aria-label="Desktop Connection">
    <Show when={connection()} fallback={<p>Checking Connection…</p>}>{current => <>
      <label class="form-label">Active Connection
        <input readonly value={current().transport === 'Unix Socket' ? 'Unix Socket · Local' : current().transport}/>
      </label>
      <label class="form-label">{current().transport === 'Unix Socket' ? 'Socket Path' : 'Server Endpoint'}
        <input readonly value={current().transport === 'Unix Socket' ? current().socket : current().endpoint}/>
      </label>
      <label class="form-label">Transport
        <select value={preference()} disabled={busy()} onChange={event => setPreference(event.currentTarget.value)}>
          <option value="auto">Automatic · Prefer Unix Socket</option>
          <option value="socket" disabled={!current().socket}>Unix Socket Only</option>
          <option value="http">HTTP(S) Only</option>
        </select>
      </label>
      <button type="button" class="button secondary" disabled={busy() || preference() === current().preference}
        onClick={() => void update(true)}>{busy() ? 'Saving…' : 'Save Connection'}</button>
      <Show when={current().pending}><p role="status">Saved. Restart Snoot to apply.</p></Show>
    </>}</Show>
    <Show when={error()}><p role="alert">{error()}</p></Show>
  </section>;
}
