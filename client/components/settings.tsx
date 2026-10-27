import { For, Show, createEffect, createSignal } from "solid-js";
import { Icon } from "./icon";
import { DesktopConnection } from "./desktop-connection";

export type Panel = "connection" | "profile" | "help";

export function Settings(props: {
  panel: Panel | undefined;
  token: string;
  onToken: (token: string) => void;
  onClose: () => void;
  sources:string[];
  onRebase:(source:string)=>Promise<void>;
}) {
  const [rebaseSource,setRebaseSource]=createSignal("");
  const [rebuilding,setRebuilding]=createSignal(false);
  const [rebaseMessage,setRebaseMessage]=createSignal("");
  const [rebaseError,setRebaseError]=createSignal("");
  const collections=()=>[...new Set(props.sources.filter(s=>s.split("/").length===4).map(s=>s.split("/").slice(0,3).join("/")+"/write"))];
  async function rebuild(){
    const source=rebaseSource();if(!source||rebuilding())return;
    const name=source.split("/").slice(0,3).join("/");
    if(!window.confirm(`Rebuild the local replica of ${name}? This clears its cached ranges and the current search view. Server data and notebook references remain. The next search will collect again.`))return;
    setRebuilding(true);setRebaseMessage("");setRebaseError("");
    try{await props.onRebase(source);setRebaseMessage("Local replica reset. Search again to collect from the server.");}
    catch(cause){setRebaseError(cause instanceof Error?cause.message:String(cause));}
    finally{setRebuilding(false);}
  }
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
                : props.panel === "profile" ? "Demo User" : "Search Guide"}
            </h2>
          </div>
          <button
            class="icon-button"
            aria-label="Close Dialog"
            onClick={() => dialog?.close()}
          >
            <Icon name="close" />
          </button>
        </div>
        <Show
          when={props.panel === "connection" || props.panel === "profile"}
          fallback={
            <div class="guide-content">
              <p>
                Search combines time, object, and field predicates. Event pages load
                as you scroll; charts describe the complete matching range.
              </p>
              <dl>
                <div>
                  <dt>Source Scope</dt>
                  <dd>
                    Choose a cache, service, resource and time window. Use level and
                    access in your query to narrow the events.
                  </dd>
                </div>
                <div>
                  <dt>Search</dt>
                  <dd>
                    Use where for exact values and ranges. search("timeout") keeps
                    literal substring matching. Add a mode for indexed full-text
                    search: search("connection timeout", terms), phrase, prefix,
                    or fuzzy. Pipeline syntax uses search "timeout" mode terms.
                  </dd>
                </div>
                <div>
                  <dt>Clear</dt>
                  <dd>
                    Remove predicates to see the selected time range again. Changing
                    the query edits your choices; press Search to apply them.
                  </dd>
                </div>
                <div>
                  <dt>Follow Live</dt>
                  <dd>
                    Continue collecting new arrivals from the server. The
                    current search applies to each new event. Stop Following to
                    keep the collection as it stands.
                  </dd>
                </div>
              </dl>
              <p class="helper">
                Times use UTC. Duration comparisons default to milliseconds. Use an explicit
                unit when needed: 500ms, 2s, or 100us. Storage retains microseconds.
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
            <p>{props.panel === "profile" ? "Your connection for this workspace. Log Out ends this tab’s session; browser-managed authentication is unchanged." : "Collect events from this trie-trunk server."}</p>
            <Show when={document.documentElement.hasAttribute('data-snoot-desktop')} fallback={<label class="form-label">
              Endpoint
              <input value={`${location.origin}/collect`} readonly />
            </label>}><DesktopConnection/></Show>
            <label class="form-label">
              Bearer Token <span>if required</span>
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
            <Show when={collections().length}>
              <hr/>
              <label class="form-label">Rebuild Local Resource Cache
                <select aria-label="Resource Cache to Rebuild" value={rebaseSource()} disabled={rebuilding()} onChange={e=>{setRebaseSource(e.currentTarget.value);setRebaseMessage("");setRebaseError("");}}>
                  <option value="">Choose a Resource</option>
                  <For each={collections()}>{source=><option value={source}>{source.split("/").slice(0,3).join("/")}</option>}</For>
                </select>
              </label>
              <button class="button secondary" type="button" disabled={!rebaseSource()||rebuilding()} onClick={()=>void rebuild()}>{rebuilding()?"Rebuilding…":"Rebuild Local Cache"}</button>
              <Show when={rebaseMessage()}><p role="status">{rebaseMessage()}</p></Show>
              <Show when={rebaseError()}><p role="alert">{rebaseError()}</p></Show>
            </Show>
          </form>
        </Show>
      </div>
    </dialog>
  );
}
