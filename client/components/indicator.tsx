import { Show, createSignal, onCleanup, onMount } from "solid-js";
import {
  compileIndicator,
  type EventField,
  type Indicator,
} from "../services/indicators";
import { Icon } from "./icon";

export function IndicatorDialog(props: {
  field: EventField;
  onSave: (indicator: Indicator) => Promise<void>;
  onClose: () => void;
}) {
  const [label, setLabel] = createSignal("");
  const [mode, setMode] = createSignal<Indicator["mode"]>("text");
  const [pattern, setPattern] = createSignal(props.field.text);
  const [ignoreCase, setIgnoreCase] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal("");
  const numeric = () => mode() === "lte" || mode() === "gte";
  let dialog!: HTMLDialogElement;
  let labelInput!: HTMLInputElement;

  onMount(() => {
    dialog.showModal();
    labelInput.focus();
  });
  onCleanup(() => dialog.close());

  async function save(event: SubmitEvent) {
    event.preventDefault();
    if (saving()) return;
    setError("");
    const indicator: Indicator = {
      id: crypto.randomUUID(),
      label: label().trim(),
      path: props.field.path,
      mode: mode(),
      pattern: pattern(),
      ignoreCase: numeric() ? false : ignoreCase(),
    };
    try {
      compileIndicator(indicator);
      setSaving(true);
      await props.onSave(indicator);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <dialog
      ref={dialog}
      class="modal indicator-modal"
      aria-labelledby="indicator-title"
      onCancel={(event) => {
        event.preventDefault();
        if (!saving()) props.onClose();
      }}
      onClick={(event) => {
        if (event.target === dialog && !saving()) props.onClose();
      }}
    >
      <form class="modal-body" onSubmit={save}>
        <div class="modal-heading">
          <h2 id="indicator-title">Add indicator</h2>
          <button
            type="button"
            class="icon-button"
            aria-label="Close indicator dialog"
            disabled={saving()}
            onClick={props.onClose}
          >
            <Icon name="close" size={16} />
          </button>
        </div>
        <div class="indicator-source">
          <code>{props.field.path.join(".")}</code>
          <pre>{props.field.text}</pre>
        </div>
        <label class="form-label">
          Label
          <input
            ref={labelInput}
            required
            maxlength={80}
            value={label()}
            onInput={(event) => setLabel(event.currentTarget.value)}
            placeholder="Server restart"
            disabled={saving()}
          />
        </label>
        <label class="form-label">
          Match
          <select
            aria-label="Match"
            value={mode()}
            onChange={(event) =>
              setMode(event.currentTarget.value as Indicator["mode"])
            }
            disabled={saving()}
          >
            <option value="text">Text contains</option>
            <option value="equals">String equals</option>
            <option value="regex">Regular expression</option>
            <option value="lte">Less than or equal to (≤)</option>
            <option value="gte">Greater than or equal to (≥)</option>
          </select>
        </label>
        <label class="form-label">
          {numeric() ? "Threshold" : mode() === "regex" ? "Pattern" : "Text"}
          <input
            type={numeric() ? "number" : "text"}
            step="any"
            required={mode() !== "equals"}
            value={pattern()}
            onInput={(event) => setPattern(event.currentTarget.value)}
            spellcheck={false}
            disabled={saving()}
          />
        </label>
        <Show when={!numeric()}>
          <label class="indicator-case">
            <input
              type="checkbox"
              checked={ignoreCase()}
              onChange={(event) => setIgnoreCase(event.currentTarget.checked)}
              disabled={saving()}
            />
            Ignore case
          </label>
        </Show>
        <p class="helper">
          <Show when={numeric()}>
            Matches numeric values only.{" "}
            <Show
              when={
                props.field.path.length === 1 &&
                props.field.path[0] === "duration"
              }
            >
              Duration is measured in microseconds.{" "}
            </Show>
          </Show>
          <Show when={mode() === "regex"}>
            Enter the expression without surrounding slashes.{" "}
          </Show>
          Labels matching events in this browser. Saved for future searches.
        </p>
        <Show when={error()}>
          <p class="indicator-error" role="alert">
            {error()}
          </p>
        </Show>
        <div class="indicator-actions">
          <button
            class="button secondary"
            type="button"
            disabled={saving()}
            onClick={props.onClose}
          >
            Cancel
          </button>
          <button class="button primary" type="submit" disabled={saving()}>
            {saving() ? "Saving…" : "Add indicator"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
