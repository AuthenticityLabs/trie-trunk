import { Show, createEffect } from "solid-js";
import { Icon } from "./icon";

export type Panel = "connection" | "help";

export function Settings(props: {
  panel: Panel | undefined;
  token: string;
  onToken: (token: string) => void;
  onClose: () => void;
}) {
  let dialog: HTMLDialogElement | undefined;
  createEffect(() => {
    if (props.panel) dialog?.showModal();
    else dialog?.close();
  });
  return (
    <dialog
      ref={dialog}
      class="modal"
      onClose={props.onClose}
      onClick={(event) => {
        if (event.target === dialog) dialog?.close();
      }}
    >
      <div class="modal-body">
        <div class="modal-heading">
          <div>
            <span class="eyebrow">WORKSPACE</span>
            <h2>
              {props.panel === "connection"
                ? "Connection"
                : "A little search fluency."}
            </h2>
          </div>
          <button
            class="icon-button"
            aria-label="Close dialog"
            onClick={() => dialog?.close()}
          >
            <Icon name="close" />
          </button>
        </div>
        <Show
          when={props.panel === "connection"}
          fallback={
            <div class="guide-content">
              <p>
                Collect events from the server, then search that working set in
                your browser.
              </p>
              <dl>
                <div>
                  <dt>Collect</dt>
                  <dd>
                    Choose a tap, time window and fields. Collect retrieves all
                    matching events and replaces the working set. The collected
                    count grows as events arrive.
                  </dd>
                </div>
                <div>
                  <dt>Search</dt>
                  <dd>
                    Use the controls below Collect to find events already in
                    this tab. The matched count shows your results alongside the
                    total collected. Search makes no server request.
                  </dd>
                </div>
                <div>
                  <dt>Clear search</dt>
                  <dd>
                    Restore the entire working set without collecting it again.
                    Changing a selector edits your choices; press Collect or
                    Search to apply them.
                  </dd>
                </div>
                <div>
                  <dt>Follow live</dt>
                  <dd>
                    Continue collecting new arrivals from the server. The
                    current search applies to each new event. Stop following to
                    keep the collection as it stands.
                  </dd>
                </div>
              </dl>
              <p class="helper">
                Times use UTC. Duration is stored in microseconds and displayed
                in a readable unit. Follow adds matching events to this tab.
              </p>
            </div>
          }
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              dialog?.close();
            }}
          >
            <p>Collect events from this trie-trunk server.</p>
            <label class="form-label">
              Endpoint
              <input value={`${location.origin}/events`} readonly />
            </label>
            <label class="form-label">
              Bearer token <span>if required</span>
              <input
                type="password"
                value={props.token}
                onInput={(event) => props.onToken(event.currentTarget.value)}
                placeholder="Paste your access token"
                autocomplete="off"
                spellcheck={false}
              />
            </label>
            <p class="helper">
              The token stays in memory for this tab only. If your server uses
              mTLS, select your client certificate in the browser. New
              collections use the current token.
            </p>
            <button class="button primary" type="submit">
              <Icon name="check" size={16} />
              Done
            </button>
          </form>
        </Show>
      </div>
    </dialog>
  );
}
