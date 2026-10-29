import type {LogEvent} from '../../client/services/dictionary';
import {eventId} from '../../client/services/dictionary';
type Notebook={id:string;name:string;createdAt:number;lastUsed:number};
const books:Notebook[]=[{id:'example',name:'Payment Investigation',createdAt:1793145600000,lastUsed:1793145600000}];
const saved=new Map<string,Set<string>>();
export async function listNotebooks(){return [...books]}
export async function savedNotebooks(event:LogEvent){return new Set(saved.get(eventId(event)))}
export async function createNotebook(name:string,event:LogEvent){const b={id:`preview-${books.length}`,name,createdAt:1793145600000,lastUsed:1793145600000};books.push(b);await saveToNotebook(b.id,event);return b}
export async function saveToNotebook(id:string,event:LogEvent){const key=eventId(event),ids=saved.get(key)??new Set<string>();ids.add(id);saved.set(key,ids)}
