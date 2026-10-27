import {measureQueryCaret,placeQueryFlyover,visibleQueryAnchor,queryFlyoverScroll} from '../services/query-flyover';
import {loadQuerySyntax,saveQuerySyntax,querySyntaxKey,querySyntaxPreference} from '../services/query-preference';
import {completionKey} from '../services/completion-keys';
import {loadEnglishWords,textCompletionContext,valueCompletions} from '../services/query-words';
import {For,Show,createMemo,createSignal,createEffect,onMount,onCleanup} from 'solid-js';
import {Portal} from 'solid-js/web';
import {Icon} from './icon';
import {completeQuery,parseQuery,formatQuery,guidedCompletion,type QuerySyntax,type QueryField,type Completion} from '../services/query';

export function QueryEditor(props:{value:string;onInput:(value:string)=>void;fields:QueryField[];loading:boolean}){
 let input:HTMLTextAreaElement|undefined,mirror:HTMLDivElement|undefined;
 let layoutFrame=0;
 const [touch,setTouch]=createSignal(false),[composing,setComposing]=createSignal(false);
 const [viewport,setViewport]=createSignal({left:0,top:0,width:0,height:0});
 const [placement,setPlacement]=createSignal({left:0,top:0,width:0,height:0});
 const [anchored,setAnchored]=createSignal(false);
 function positionFlyover(reveal: unknown = false){
  cancelAnimationFrame(layoutFrame);
  layoutFrame=requestAnimationFrame(()=>{
   if(!input||!mirror||!visible()||document.activeElement!==input){setAnchored(false);return}
   const visual=window.visualViewport;
   const view={left:visual?.offsetLeft??0,top:visual?.offsetTop??0,width:visual?.width??window.innerWidth,height:visual?.height??window.innerHeight};
   setViewport(view);
   const editorBox=input.getBoundingClientRect();
   const desiredHeight=Math.min(menu().length,4)*(touch()?44:34)+10+(touch()&&valid()?44:0);
   if(touch()){
    // Mobile suggestions are a normal-flow sibling of the textarea. Safari can
    // pan its visual viewport independently while the keyboard animates; fixed
    // portal coordinates must never determine their position relative to text.
    setAnchored(input.selectionStart===input.selectionEnd);
    if(reveal===true){
     const shift=queryFlyoverScroll(editorBox,view,Math.min(desiredHeight,mobileMenuHeight()));
     if(shift>1)input.closest('.page-scroll')?.scrollBy({top:shift,behavior:'instant'});
    }
    return;
   }
   const caretBox=visibleQueryAnchor(measureQueryCaret(input,mirror),editorBox,view,touch());
   if(!caretBox||input.selectionStart!==input.selectionEnd){setAnchored(false);return}
   const next=placeQueryFlyover(caretBox,view,desiredHeight);
   setPlacement(next);setAnchored(next.height>=(touch()?54:44)&&next.width>0);
  });
 }
 onMount(()=>{
  mirror=document.createElement('div');mirror.setAttribute('aria-hidden','true');document.body.append(mirror);
  const media=window.matchMedia('(pointer: coarse)'),visual=window.visualViewport;
  const measure=()=>{setTouch(media.matches||(visual?.width??window.innerWidth)<=680);setViewport({left:visual?.offsetLeft??0,top:visual?.offsetTop??0,width:visual?.width??window.innerWidth,height:visual?.height??window.innerHeight});positionFlyover(true)};
  const selection=()=>{if(document.activeElement===input){setCaret(input!.selectionStart);positionFlyover()}};
  const observer=new ResizeObserver(()=>positionFlyover(true));if(input)observer.observe(input);
  measure();media.addEventListener('change',measure);window.addEventListener('resize',measure);window.addEventListener('scroll',positionFlyover,true);visual?.addEventListener('resize',measure);visual?.addEventListener('scroll',measure);document.addEventListener('selectionchange',selection);
  onCleanup(()=>{cancelAnimationFrame(layoutFrame);observer.disconnect();mirror?.remove();media.removeEventListener('change',measure);window.removeEventListener('resize',measure);window.removeEventListener('scroll',positionFlyover,true);visual?.removeEventListener('resize',measure);visual?.removeEventListener('scroll',measure);document.removeEventListener('selectionchange',selection)});
 });
 const [typed,setTyped]=createSignal(false),[menuOpen,setMenuOpen]=createSignal(false),[words,setWords]=createSignal<readonly string[]>([]),[wordState,setWordState]=createSignal('');
 const [caret,setCaret]=createSignal(0),[focused,setFocused]=createSignal(false),[active,setActive]=createSignal(-1),[preferred,setPreferred]=createSignal<QuerySyntax>(loadQuerySyntax());
 const [preferenceError,setPreferenceError]=createSignal('');
 onMount(()=>{
  const sync=(event:StorageEvent)=>{if(event.storageArea===sessionStorage&&(event.key===querySyntaxKey||event.key===null))setPreferred(querySyntaxPreference(event.newValue))};
  window.addEventListener('storage',sync);onCleanup(()=>window.removeEventListener('storage',sync));
 });
 const textContext=createMemo(()=>textCompletionContext(props.value,caret(),props.fields));
 createEffect(()=>{const c=textContext();if(c?.word&&!c.field?.values.length&&menuOpen()&&!words().length&&!wordState()){setWordState('Loading word suggestions…');void loadEnglishWords().then(w=>{setWords(w);setWordState('')},()=>setWordState('Word suggestions unavailable; continue typing.'))}});
 const syntax=()=>props.value.trim()?(/^\s*events\./.test(props.value)?'builder':'pipeline'):preferred();
 const parsed=createMemo(()=>{try{return {ast:parseQuery(props.value,props.fields),error:''}}catch(e){return {ast:undefined,error:e instanceof Error?e.message:String(e)}}});
 const options=createMemo(()=>(textContext()?valueCompletions(props.value,caret(),props.fields,words()):completeQuery(props.value,caret(),props.fields)).filter(c=>{const next=guidedCompletion(props.value,c);return props.value.slice(0,next.from)+next.insert+props.value.slice(next.to)!==props.value}));
 const valid=()=>!props.loading&&!!parsed().ast;
 type MenuItem={label:string;detail:string;choose:()=>void;disabled?:boolean};
 const actions=():MenuItem[]=>[
  {label:'+ Filter',detail:'where',choose:()=>append('where'),disabled:!valid()},
  {label:'+ Text',detail:'search',choose:()=>append('search'),disabled:!valid()||!!parsed().ast?.plan.search},
  {label:'+ Sort',detail:'order',choose:()=>append('order'),disabled:!valid()},
  {label:'+ Page Size',detail:'page',choose:()=>append('page'),disabled:!valid()||!!parsed().ast?.page},
  {label:'Text Mode',detail:parsed().ast?.plan.search?.mode??'search',choose:()=>chooseMode(),disabled:!valid()||!parsed().ast?.plan.search},
 ];
 const menu=createMemo<MenuItem[]>(()=>{
  if(options().length)return options().map(c=>({label:c.label,detail:c.detail,choose:()=>choose(c)}));
  if(textContext()){
   const context=textContext()!;
   if(context.typed)return [{label:'Use Text',detail:'Complete value',choose:()=>{
    const c:Completion={label:context.typed,detail:'Definition value',insert:JSON.stringify(context.typed),from:context.quote,to:context.end};choose(c);
   }}];
   return [];
  }
  if(caret()!==props.value.length)return [];
  if(!valid()&&syntax()==='builder'){
   try{parseQuery(props.value+')',props.fields);return [{label:')',detail:'Complete clause',choose:()=>edit(props.value+') ')}]}catch{}
  }
  return actions().filter(a=>!a.disabled);
 });
 function move(index:number){setFocused(true);setMenuOpen(true);setActive(index);queueMicrotask(()=>{
  const item=document.getElementById(`query-option-${index}`),rail=item?.closest<HTMLElement>('.query-completion-menu');
  if(item&&rail){const bounds=item.getBoundingClientRect(),viewport=rail.getBoundingClientRect();if(bounds.top<viewport.top)rail.scrollTop+=bounds.top-viewport.top;else if(bounds.bottom>viewport.bottom)rail.scrollTop+=bounds.bottom-viewport.bottom}
 })}

 function edit(value:string,position=value.length){props.onInput(value);queueMicrotask(()=>{input?.focus({preventScroll:true});input?.setSelectionRange(position,position);setCaret(position);setFocused(true);setMenuOpen(true);setTyped(false);setActive(-1);positionFlyover(true)})}
 function refresh(){setCaret(input?.selectionStart??props.value.length);setActive(-1)}
 function choose(c:Completion){const next=guidedCompletion(props.value,c);edit(props.value.slice(0,next.from)+next.insert+props.value.slice(next.to),next.from+(next.cursor??next.insert.length))}
 function changeSyntax(next:QuerySyntax){if(!valid())return;setPreferred(next);setPreferenceError(saveQuerySyntax(next)?'':'This browser could not save the syntax preference.');edit(formatQuery(parsed().ast!,next))}
 function chooseMode(){
  const fluent=syntax()==='builder';
  const pattern=fluent?/\.search\((\s*"(?:[^"\\]|\\.)*")\s*(?:,\s*\w+)?\)/:/\bsearch\s+("(?:[^"\\]|\\.)*")(?:\s+mode\s+\w+)?/;
  const match=pattern.exec(props.value);if(!match)return;
  const prefix=(fluent?'.search(':'search ')+match[1]+(fluent?', ':' mode ');
  edit(props.value.slice(0,match.index)+prefix+(fluent?')':'')+props.value.slice(match.index+match[0].length),match.index+prefix.length);
 }
 function append(stage:'where'|'search'|'order'|'page'){
  if(!valid())return;
  const ast=structuredClone(parsed().ast!),mode=syntax();
  const page=ast.page;delete ast.page;
  if(stage==='order')ast.plan.order='asc';
  const prefix=formatQuery(ast,mode);
  const fragment=mode==='builder'?{where:'.where(',search:'.search("")',order:'.order(timestamp, ',page:'.page('}[stage]:{where:'| where ',search:'| search ""',order:'| order timestamp ',page:'| page '}[stage];
  const text=prefix+'\n  '+fragment;
  const suffix=page&&stage!=='page'?formatQuery({collection:'events',plan:{filters:[],order:'asc'},page},mode).slice((mode==='builder'?'events.query()':'events').length):'';
  edit(text+suffix,stage==='search'?text.length-(mode==='builder'?2:1):text.length);
 }
 const hint=()=>{
  const before=props.value.slice(0,caret());
  if(textContext())return textContext()?.field?.values.length?'Choose a definition value, or enter your own text.':wordState()||'Free text · English word suggestions are optional.';
  if(!props.value.trim())return '';
  if(/where\(\s*$|\|\s*where\s*$/.test(before))return 'Choose a field from this resource.';
  if(options().some(c=>c.detail==='Search Mode'))return 'Contains: literal text · Terms: all words · Phrase: word order · Prefix: final word prefix · Fuzzy: one edit per word.';
  if(options().some(c=>c.detail==='Comparison'))return 'Choose a comparison.';
  if(options().some(c=>['Mapped value','Boolean'].includes(c.detail)))return 'Choose a value or type your own.';
  if(!valid())return focused()?'Continue the expression · strings use quotes':parsed().error;
  return touch()?'Tap a suggestion to continue, or Search when ready.':'Add a clause, or press Enter to search.';
 };
 const Suggestions=()=> <div id="query-completions" class="query-completion-menu" role="listbox" aria-label="Query Suggestions"><For each={menu()}>{(c,i)=><button type="button" role="option" id={`query-option-${i()}`} aria-selected={active()===i()} data-suggested={active()<0&&i()===0?true:undefined} onPointerDown={e=>e.preventDefault()} onMouseDown={e=>e.preventDefault()} onClick={c.choose}><span>{c.label}</span>{" "}<small>{c.detail}</small></button>}</For></div>;
 const visible=()=>focused()&&menuOpen()&&!composing();
 const mobileMenuHeight=()=>Math.min(230,Math.max(98,viewport().height*.4));
 createEffect(()=>{props.value;caret();visible();menu().length;valid();touch();positionFlyover(true)});
 function submit(){if(!valid())return;setMenuOpen(false);input?.blur();setFocused(false);input?.form?.requestSubmit()}
 return <section class="query-composer" classList={{'query-touch':touch()}} aria-label="Query Builder">
  <div class="query-composer-toolbar">
   <div class="query-syntax-tabs" role="group" aria-label="Query Syntax">
    <button type="button" aria-pressed={syntax()==='pipeline'} disabled={!valid()} onClick={()=>changeSyntax('pipeline')}>Pipeline</button>
    <button type="button" aria-pressed={syntax()==='builder'} disabled={!valid()} onClick={()=>changeSyntax('builder')}>Builder</button>
   </div>
   <Show when={props.loading}><span class="query-editor-state" role="status">Loading definition…</span></Show>
  </div>
  <label class="sr-only" for="snoot-query">Search Query</label>
  <textarea id="snoot-query" ref={input} role="combobox" aria-autocomplete="list" aria-controls="query-completions" aria-expanded={visible()&&anchored()&&menu().length>0}
   aria-activedescendant={visible()&&anchored()&&active()>=0?`query-option-${active()}`:undefined} aria-invalid={!focused()&&!!parsed().error} aria-describedby={hint()?'query-syntax-status':undefined}
   style={touch()?{'max-height':`${Math.max(96,viewport().height-180)}px`}:undefined}
   autocomplete="off" autocapitalize="off" autocorrect="off" enterkeyhint="search" spellcheck={false} rows={Math.min(7,Math.max(3,props.value.split('\n').length+1))} value={props.value} placeholder={syntax()==='builder'?'events.query()\n  .where(…)':'events\n  | where …'}
   onInput={e=>{const value=e.currentTarget.value,position=e.currentTarget.selectionStart;props.onInput(value);setCaret(position);setActive(-1);setTyped(true);setMenuOpen(true);positionFlyover(true)}} onClick={()=>{refresh();setTyped(false);setMenuOpen(true);positionFlyover(true)}} onFocus={()=>{setFocused(true);refresh();setMenuOpen(true);positionFlyover(true)}} onBlur={()=>{setFocused(false);setMenuOpen(false);setAnchored(false)}}
   onCompositionStart={()=>setComposing(true)} onCompositionEnd={()=>{setComposing(false);refresh();setTyped(true);setMenuOpen(true)}}
   onKeyUp={e=>{if(input?.selectionStart!==caret()){refresh();setFocused(true)}}}
   onKeyDown={e=>{
    if(e.isComposing||composing())return;
    if(e.key==='Escape'){setMenuOpen(false);setActive(-1);e.preventDefault();return}
    if((e.key==='ArrowDown'||e.key==='ArrowUp')&&textContext()&&!menu().length&&!e.shiftKey&&!e.ctrlKey&&!e.metaKey&&!e.altKey){e.preventDefault();setMenuOpen(true);return}
    const decision=completionKey({key:e.key,shift:e.shiftKey,modified:e.ctrlKey||e.metaKey||e.altKey,composing:e.isComposing,open:focused()&&menuOpen(),engaged:typed()&&options().length>0,count:menu().length,active:active(),collapsed:input?.selectionStart===input?.selectionEnd,atEnd:input?.selectionStart===props.value.length||!!textContext()&&input?.selectionStart===textContext()?.wordEnd});
    if(decision){e.preventDefault();if(decision.kind==='move'||decision.kind==='open')move(decision.index);else menu()[decision.index]?.choose();return}
    if(e.key==='Enter'&&!e.shiftKey&&!e.ctrlKey&&!e.metaKey&&!e.altKey){e.preventDefault();submit()}
   }}/>
  <Show when={touch()&&visible()&&anchored()&&menu().length>0}>
   <div class="query-flyover query-flyover-touch query-completions-inline" role="region" aria-label="Search Suggestions" style={{'max-height':`${mobileMenuHeight()}px`}}>
    <Suggestions/>
    <Show when={valid()}><div class="query-flyover-footer"><button type="button" class="button primary" onPointerDown={e=>e.preventDefault()} onMouseDown={e=>e.preventDefault()} onClick={submit}><Icon name="search" size={16}/>Search</button></div></Show>
   </div>
  </Show>
  <Show when={preferenceError()}><p role="status">{preferenceError()}</p></Show>
  <div class="query-assist" classList={{'query-assist-quiet':touch()&&(focused()||!parsed().error)}}>
   <Show when={!touch()&&visible()&&menu().length>0}><Portal>
    <div class="query-flyover" role="region" aria-label="Search Suggestions"
     style={{left:`${placement().left}px`,top:`${placement().top}px`,width:`${placement().width}px`,'max-height':`${placement().height}px`,visibility:anchored()?'visible':'hidden'}}>
     <Suggestions/>
    </div>
   </Portal></Show>
   <Show when={hint()}><div id="query-syntax-status" class="query-assist-hint" classList={{'sr-only':touch()&&(focused()||!parsed().error)}}>{hint()}</div></Show>
   <Show when={!touch()}><div class="query-assist-rail"><div class="query-actions"><For each={actions()}>{a=><button type="button" disabled={a.disabled} onClick={a.choose}>{a.label}</button>}</For></div></div></Show>
  </div>
 </section>
}
