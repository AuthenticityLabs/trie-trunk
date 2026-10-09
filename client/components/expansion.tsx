import { For, Show, onCleanup, onMount } from "solid-js";
import { describeInterval, type Interval } from "../services/coverage";
import { Icon } from "./icon";

export function ExpansionDialog(props: {
  ranges: Interval[];
  scope: string;
  running: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  let dialog!: HTMLDialogElement;
  onMount(() => dialog.showModal());
  onCleanup(() => dialog.close());
  return (
    <dialog
      ref={dialog}
      class="modal expansion-modal"
      aria-labelledby="expansion-title"
      onCancel={(event) => {
        event.preventDefault();
        props.onClose();
      }}
      onClick={(event) => {
        if (event.target === dialog) props.onClose();
      }}
    >
      <div class="modal-body">
        <div class="modal-heading">
          <h2 id="expansion-title">Expand collection set?</h2>
          <button
            type="button"
            class="icon-button"
            aria-label="Close expansion dialog"
            onClick={props.onClose}
          >
            <Icon name="close" size={16} />
          </button>
        </div>
        <p>
          Append events from these uncollected ranges. Events already collected
          stay in this tab.
        </p>
        <ul class="expansion-ranges">
          <For each={props.ranges}>
            {(range) => <li>{describeInterval(range)}</li>}
          </For>
        </ul>
        <p class="expansion-scope">{props.scope}</p>
        <Show when={props.running}>
          <p>
            The current stream will stop before expansion. Incomplete ranges may
            be requested again; duplicate events are ignored.
          </p>
        </Show>
        <div class="expansion-actions">
          <button
            type="button"
            class="button secondary"
            autofocus
            onClick={props.onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            class="button primary"
            onClick={props.onConfirm}
          >
            {props.running ? "Stop and expand" : "Expand collection"}
          </button>
        </div>
      </div>
    </dialog>
  );
}
