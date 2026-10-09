import { For, Show, createSignal, onCleanup, onMount } from "solid-js";
import { Portal } from "solid-js/web";
import {
  createNotebook,
  listNotebooks,
  savedNotebooks,
  saveToNotebook,
  type Notebook,
} from "../services/notebooks";
import type { LogEvent } from "../services/dictionary";
import { Icon } from "./icon";

export function NotebookPicker(props: {
  event: LogEvent;
  anchor: HTMLElement;
  onSaved: (name: string) => void;
  onClose: () => void;
}) {
  const [notebooks, setNotebooks] = createSignal<Notebook[]>([]);
  const [saved, setSaved] = createSignal(new Set<string>());
  const [loading, setLoading] = createSignal(true);
  const [creating, setCreating] = createSignal(false);
  const [name, setName] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  let dialog!: HTMLDialogElement;
  let disposed = false;

  function position() {
    const anchor = props.anchor.getBoundingClientRect();
    const width = dialog.offsetWidth;
    const height = dialog.offsetHeight;
    dialog.style.left = `${Math.max(12, Math.min(anchor.right - width, window.innerWidth - width - 12))}px`;
    const top =
      anchor.bottom + 8 + height <= window.innerHeight - 12
        ? anchor.bottom + 8
        : anchor.top - height - 8;
    dialog.style.top = `${Math.max(12, Math.min(top, window.innerHeight - height - 12))}px`;
  }

  async function load() {
    setLoading(true);
    setError("");
    try {
      const [items, membership] = await Promise.all([
        listNotebooks(),
        savedNotebooks(props.event),
      ]);
      if (!disposed) {
        setNotebooks(items);
        setSaved(membership);
      }
    } catch (cause) {
      if (!disposed)
        setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (!disposed) setLoading(false);
    }
  }

  onMount(() => {
    dialog.showModal();
    position();
    const observer = new ResizeObserver(position);
    observer.observe(dialog);
    window.addEventListener("resize", position);
    void load();
    onCleanup(() => {
      disposed = true;
      observer.disconnect();
      window.removeEventListener("resize", position);
      dialog.close();
      if (props.anchor.isConnected) props.anchor.focus({ preventScroll: true });
    });
  });

  async function save(notebook?: Notebook) {
    if (busy()) return;
    setBusy(true);
    setError("");
    try {
      let title: string;
      if (notebook) {
        await saveToNotebook(notebook.id, props.event);
        title = notebook.name;
      } else {
        title = (await createNotebook(name(), props.event)).name;
      }
      if (!disposed) props.onSaved(title);
    } catch (cause) {
      if (!disposed)
        setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (!disposed) setBusy(false);
    }
  }

  return (
    <Portal>
      <dialog
        ref={dialog}
        class="notebook-menu"
        aria-labelledby="notebook-title"
        onCancel={(event) => {
          event.preventDefault();
          if (!busy()) props.onClose();
        }}
        onClick={(event) => {
          if (event.target === dialog && !busy()) props.onClose();
        }}
      >
        <div class="notebook-heading">
          <strong id="notebook-title">Save to notebook</strong>
          <button
            type="button"
            class="icon-button"
            aria-label="New notebook"
            title="New notebook"
            disabled={busy()}
            onClick={() => setCreating(true)}
          >
            <Icon name="plus" size={16} />
          </button>
          <button
            type="button"
            class="icon-button"
            aria-label="Close notebook menu"
            disabled={busy()}
            onClick={props.onClose}
          >
            <Icon name="close" size={16} />
          </button>
        </div>
        <Show when={creating()}>
          <form
            class="notebook-create"
            onSubmit={(event) => {
              event.preventDefault();
              void save();
            }}
          >
            <label class="form-label">
              Notebook name
              <input
                ref={(input) => queueMicrotask(() => input.focus())}
                required
                value={name()}
                onInput={(event) => setName(event.currentTarget.value)}
                placeholder="Incident or performance analysis"
                disabled={busy()}
              />
            </label>
            <div class="notebook-create-actions">
              <button
                class="text-button"
                type="button"
                disabled={busy()}
                onClick={() => setCreating(false)}
              >
                Cancel
              </button>
              <button class="button primary" type="submit" disabled={busy()}>
                {busy() ? "Saving…" : "Create & save"}
              </button>
            </div>
          </form>
        </Show>
        <Show when={loading()}>
          <p class="notebook-hint" role="status">
            Loading notebooks…
          </p>
        </Show>
        <Show
          when={!loading() && !notebooks().length && !error() && !creating()}
        >
          <p class="notebook-hint">
            Keep evidence together for an incident or performance analysis.
          </p>
          <button
            class="notebook-option"
            disabled={busy()}
            onClick={() => setCreating(true)}
          >
            <Icon name="plus" size={16} /> Create your first notebook
          </button>
        </Show>
        <div
          class="notebook-list"
          aria-label="Notebooks, most recently used first"
        >
          <For each={notebooks()}>
            {(notebook) => (
              <button
                class="notebook-option"
                disabled={busy()}
                onClick={() => void save(notebook)}
                aria-label={`Save to ${notebook.name}${saved().has(notebook.id) ? " (already saved)" : ""}`}
              >
                <Icon name="notebook" size={16} />
                <span>{notebook.name}</span>
                <Show when={saved().has(notebook.id)}>
                  <small>Saved</small>
                  <Icon name="check" size={14} />
                </Show>
              </button>
            )}
          </For>
        </div>
        <Show when={error()}>
          <p class="notebook-error" role="alert">
            {error()}{" "}
            <button
              class="text-button"
              onClick={() => void load()}
              disabled={busy()}
            >
              Retry
            </button>
          </p>
        </Show>
      </dialog>
    </Portal>
  );
}
