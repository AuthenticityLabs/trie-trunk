import {completionKey} from '../services/completion-keys';
import {For,Show,createSignal,createMemo} from 'solid-js';
import {sourceScope,scopeSource,sourceParts,sourceOptions} from '../services/source';
export function Sources(props:{value:string;catalog:readonly string[];loading:boolean;onChange:(value:string)=>void}){
 const [editing,setEditing]=createSignal<number>(),[draft,setDraft]=createSignal(''),[active,setActive]=createSignal(-1);
 let input:HTMLInputElement|undefined;
 const scope=()=>sourceScope(props.value);
 const labels=['Cache','Service','Resource'],all=['All Caches','All Services','All Resources'];
 const choices=createMemo(()=>editing()===undefined?[]:sourceOptions(props.catalog,scope(),editing()!).filter(v=>v.toLowerCase().includes(draft().toLowerCase())));
 function open(index:number){setEditing(index);setDraft('');setActive(-1);queueMicrotask(()=>input?.focus())}
 function choose(value:string){const index=editing()!;const next={...scope(),[sourceParts[index]]:value};for(const later of sourceParts.slice(index+1))delete next[later];props.onChange(scopeSource(next,props.catalog));if(value&&index<sourceParts.length-1)open(index+1);else setEditing(undefined)}
 return <div class="source-breadcrumb">
  <span class="source-caption">From</span>
  <For each={sourceParts}>{(key,i)=><><Show when={i()>0}><span class="source-divider">/</span></Show><button type="button" aria-label={`${labels[i()]}: ${scope()[key]||all[i()]}`} aria-expanded={editing()===i()} classList={{'is-editing':editing()===i(),'is-all':!scope()[key]}} onClick={()=>editing()===i()?setEditing(undefined):open(i())}>{scope()[key]||all[i()]}</button></>}</For>
  <Show when={editing()!==undefined}>
   <div class="source-discovery">
    <input ref={input} role="combobox" aria-label={`Find ${labels[editing()!]}`} aria-autocomplete="list" aria-controls="source-suggestions" aria-expanded={true} aria-activedescendant={active()>=0?`source-choice-${active()}`:undefined} placeholder={`Find ${labels[editing()!].toLowerCase()}…`} value={draft()} autocomplete="off"
     onInput={e=>{setDraft(e.currentTarget.value);setActive(-1)}} onKeyDown={e=>{
      if(e.isComposing)return;
      if(e.key==='Escape'){e.preventDefault();setEditing(undefined);return}
      const decision=completionKey({key:e.key,shift:e.shiftKey,modified:e.ctrlKey||e.metaKey||e.altKey,composing:e.isComposing,open:true,engaged:!!draft(),count:choices().length,active:active(),collapsed:input?.selectionStart===input?.selectionEnd,atEnd:input?.selectionStart===draft().length});
      if(decision){e.preventDefault();if(decision.kind==='accept')choose(choices()[decision.index]);else {setActive(decision.index);queueMicrotask(()=>{const item=document.getElementById(`source-choice-${decision.index}`),rail=item?.parentElement;if(item&&rail){const a=item.getBoundingClientRect(),b=rail.getBoundingClientRect();if(a.left<b.left)rail.scrollLeft+=a.left-b.left;else if(a.right>b.right)rail.scrollLeft+=a.right-b.right}})}}
      else if(e.key==='Enter')e.preventDefault();
     }}/>
    <button type="button" class="source-any" onClick={()=>choose('')}>{all[editing()!]}</button>
    <div id="source-suggestions" role="listbox" aria-label={`${labels[editing()!]} suggestions`}><For each={choices()}>{(value,i)=><button type="button" role="option" id={`source-choice-${i()}`} aria-selected={active()===i()} data-suggested={active()<0&&i()===0?true:undefined} onMouseDown={e=>e.preventDefault()} onClick={()=>choose(value)}>{value}</button>}</For><Show when={!choices().length}><span>{props.loading?'Loading catalog…':'No matches'}</span></Show></div>
    <button type="button" aria-label="Close Source Suggestions" onClick={()=>setEditing(undefined)}>×</button>
   </div>
  </Show>
 </div>
}
