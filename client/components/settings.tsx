import {createEffect} from 'solid-js';
import {Icon} from './icon';
export type Panel='help';
export function Settings(props:{panel:Panel|undefined;onClose:()=>void}){
 let dialog!:HTMLDialogElement;
 createEffect(()=>{if(props.panel)dialog.showModal();else dialog.close()});
 return <dialog ref={dialog} class="modal" aria-label="Search Guide" onClose={props.onClose} onClick={event=>{if(event.target===dialog)dialog.close()}}>
  <div class="modal-body"><div class="modal-heading"><h2>Search Guide</h2><button type="button" class="icon-button" aria-label="Close Dialog" onClick={()=>dialog.close()}><Icon name="close"/></button></div>
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
  </div>
 </dialog>;
}
