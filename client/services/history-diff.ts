import { expandEvent, jsonText, type LogEvent } from './dictionary.ts';

export type HistoryChange = {name:string;kind:'observed'|'changed';before?:unknown;after:unknown};
export type HistoryDiff = {changes:HistoryChange[];supplied:number};
export function sameHistoryValue(a:unknown,b:unknown):boolean {
  if(Object.is(a,b))return true;
  if(!a||!b||typeof a!=='object'||typeof b!=='object')return false;
  if(Array.isArray(a)!==Array.isArray(b))return false;
  const left=Object.keys(a),right=Object.keys(b);
  return left.length===right.length&&left.every(key=>Object.hasOwn(b,key)&&sameHistoryValue((a as Record<string,unknown>)[key],(b as Record<string,unknown>)[key]));
}
export const historyValue=(value:unknown):string=>jsonText(value);

// Retained events can begin mid-history. First observation is not proof of creation.
// Omission is not deletion. Compare only values actually supplied by each event.
export class ChangeHistory {
  private previous=new Map<string,unknown>();
  private rows=new WeakMap<LogEvent,HistoryDiff>();
  append(event:LogEvent):void {
    const d=event.dictionary;
    const metadata=new Set([d.id.name,d.objectIdentity?.field.name,d.revisionIdentity?.name,'time']);
    const changes:HistoryChange[]=[];let supplied=0;
    for(const [name,value] of Object.entries(expandEvent(event))){
      if(metadata.has(name)||value===undefined)continue;
      supplied++;
      if(!this.previous.has(name))changes.push({name,kind:'observed',after:value});
      else if(!sameHistoryValue(this.previous.get(name),value))changes.push({name,kind:'changed',before:this.previous.get(name),after:value});
      this.previous.set(name,value);
    }
    this.rows.set(event,{changes,supplied});
  }
  get(event:LogEvent):HistoryDiff {return this.rows.get(event)??{changes:[],supplied:0}}
}
