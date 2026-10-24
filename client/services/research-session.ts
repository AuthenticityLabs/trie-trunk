import { readTab, writeTab } from './search-session.ts';
import { validPosition, type ScrollPosition } from './ui-position.ts';
export type ResearchDraft={source:string;id:string;from:string;to:string};
export type ResearchState={draft:ResearchDraft;query?:ResearchDraft;visible:number;selected?:string;position:ScrollPosition;archiveLeft?:number};
const key=(mode:string,scope:string)=>`snoot-research:${scope}:${mode}`;
export function readResearch(mode:string,scope:string):ResearchState|undefined {
 const state=readTab<ResearchState>(key(mode,scope));
 const draft=(v:ResearchDraft)=>v&&['source','id','from','to'].every(k=>typeof v[k as keyof ResearchDraft]==='string');
 if(!state||(state.archiveLeft!==undefined&&(!Number.isFinite(state.archiveLeft)||state.archiveLeft<0))||!draft(state.draft)||(state.query&&!draft(state.query))||!validPosition(state.position)||
  !Number.isSafeInteger(state.visible)||state.visible<1||(state.selected!==undefined&&typeof state.selected!=='string'))return;
 return state;
}
export function writeResearch(mode:string,scope:string,state:ResearchState){return writeTab(key(mode,scope),state)}
