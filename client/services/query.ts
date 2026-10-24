/** Read-only syntax: never evaluated as JavaScript. Decimal literals remain exact. */
export type QueryField={name:string;type:string;values:string[]};
export type Literal={kind:'string'|'number'|'boolean'|'null';value:string};
export type Filter={field:string;op:'=='|'!='|'>'|'>='|'<'|'<='|'contains';value:Literal};
export type SearchMode='contains'|'terms'|'phrase'|'prefix'|'fuzzy';
export const searchModes:SearchMode[]=['contains','terms','phrase','prefix','fuzzy'];
export type QueryPlan={filters:Filter[];search?:{text:string;mode:SearchMode};order:'asc'|'desc'};
export type QueryAST={collection:'events';plan:QueryPlan;page?:{size:number;after?:string}};
type Token={text:string;kind:'word'|'string'|'number'|'symbol';at:number;end:number};
export class QueryError extends Error {at:number;constructor(message:string,at:number){super(`${message} (column ${at+1})`);this.at=at}}
function lex(text:string):Token[]{
 const out:Token[]=[];let at=0;
 while(at<text.length){if(/\s/.test(text[at])){at++;continue}const start=at;let match:RegExpMatchArray|null;
  if(text[at]==='"'){
   at++;let escaped=false;while(at<text.length){const c=text[at++];if(c==='"'&&!escaped)break;if(c==='\\'&&!escaped)escaped=true;else escaped=false}
   const raw=text.slice(start,at);try{const value=JSON.parse(raw);if(typeof value!=='string')throw Error();out.push({text:value,kind:'string',at:start,end:at})}catch{throw new QueryError('Unclosed or invalid quoted string',start)}
  }else if((match=text.slice(at).match(/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/))){at+=match[0].length;out.push({text:match[0],kind:'number',at:start,end:at})}
  else if((match=text.slice(at).match(/^[A-Za-z_@][\w@-]*/))){at+=match[0].length;out.push({text:match[0],kind:'word',at:start,end:at})}
  else if((match=text.slice(at).match(/^(?:==|!=|>=|<=|[><|.,()])/))){at+=match[0].length;out.push({text:match[0],kind:'symbol',at:start,end:at})}
  else throw new QueryError(`Unexpected ${JSON.stringify(text[at])}`,at);
 }
 return out;
}
export function literal(value:unknown):Literal{
 if(value===null)return {kind:'null',value:''};if(typeof value==='boolean')return {kind:'boolean',value:String(value)};
 if(typeof value==='bigint')return {kind:'number',value:String(value)};
 if(typeof value==='number'){if(!Number.isFinite(value)||Number.isInteger(value)&&!Number.isSafeInteger(value))throw Error('Use bigint for large integers');return {kind:'number',value:String(value)}}
 if(typeof value==='string')return {kind:'string',value};throw Error('Unsupported literal');
}
export function parseQuery(text:string,fields:readonly QueryField[]):QueryAST{
 if(text.length>8192)throw new QueryError('Query too long',8192);
 const tokens=lex(text);let i=0;const result:QueryAST={collection:'events',plan:{filters:[],order:'asc'}};
 const fail=(message:string):never=>{throw new QueryError(message,tokens[i]?.at??text.length)};
 const take=()=>tokens[i++]??fail('Incomplete query');
 const expect=(value:string)=>{if(tokens[i]?.text!==value)fail(`Expected ${value}`);i++};
 const field=()=>{const t=take();if(t.kind!=='word'&&t.kind!=='string')fail('Expected field name');return t.text};
 const value=(name:string):Literal=>{const t=take();if(t.kind==='string')return {kind:'string',value:t.text};if(t.kind==='number')return {kind:'number',value:t.text};if(t.text==='true'||t.text==='false')return {kind:'boolean',value:t.text};if(t.text==='null')return {kind:'null',value:''};if(t.kind==='word'&&fields.some(f=>f.name===name&&f.values.includes(t.text)))return {kind:'string',value:t.text};return fail('Quote arbitrary strings; unquoted values must be mapped values')};
 if(tokens.length===0)return result;
 if(tokens[0].kind==='string'){if(!tokens[0].text)fail('search requires nonempty text');result.plan.search={text:take().text,mode:'contains'};if(i!==tokens.length)fail('Unexpected suffix');return result}
 expect('events');const fluent=tokens[i]?.text==='.';
 if(fluent){expect('.');expect('query');expect('(');expect(')')}else expect('query');
 let ordered=false,searched=false,paged=false;
 while(i<tokens.length){expect(fluent?'.':'|');const op=take().text;if(fluent)expect('(');
  if(paged)fail('page must be the last stage');
  if(op==='where'){
   const name=field();if(!fields.some(f=>f.name===name))fail(`Unknown field ${name}`);
   if(fluent)expect(',');const operator=take().text;if(!['==','!=','>','>=','<','<=','contains'].includes(operator))fail('Unsupported comparison');if(fluent)expect(',');
   result.plan.filters.push({field:name,op:operator as Filter['op'],value:value(name)});
   if(result.plan.filters.length>32)fail('At most 32 predicates');
  }else if(op==='search'){
   if(searched)fail('Only one search stage');searched=true;const t=take();if(t.kind!=='string'||!t.text)fail('search requires quoted text');let mode:SearchMode='contains';if(fluent?tokens[i]?.text===',':tokens[i]?.text==='mode'){i++;const m=take().text;if(!searchModes.includes(m as SearchMode))fail('Expected contains, terms, phrase, prefix, or fuzzy');mode=m as SearchMode}result.plan.search={text:t.text,mode};
  }else if(op==='order'){
   if(ordered)fail('Only one order stage');ordered=true;if(field()!=='timestamp')fail('Only timestamp ordering is supported');if(fluent)expect(',');const direction=take().text;if(direction!=='asc'&&direction!=='desc')fail('Expected asc or desc');result.plan.order=direction as 'asc'|'desc';
  }else if(op==='page'){
   paged=true;const t=take(),size=Number(t.text);if(t.kind!=='number'||!Number.isInteger(size)||size<1||size>1000)fail('page size must be 1–1000');result.page={size};
   if(tokens[i]?.text===(fluent?',':'after')){i++;const cursor=take();if(cursor.kind!=='string'&&cursor.kind!=='word')fail('Expected cursor');result.page.after=cursor.text}
  }else fail(`Unsupported read-only stage ${op}`);
  if(fluent)expect(')');
 }
 return result;
}
export class EventQuery {
 private ast:QueryAST={collection:'events',plan:{filters:[],order:'asc'}};
 where(field:string,op:Filter['op'],value:unknown){this.ast.plan.filters.push({field,op,value:literal(value)});return this}
 search(text:string,mode:SearchMode='contains'){if(!text||!searchModes.includes(mode))throw Error('Invalid search');this.ast.plan.search={text,mode};return this}
 order(field:'timestamp',direction:'asc'|'desc'){if(field!=='timestamp'||!['asc','desc'].includes(direction))throw Error('Unsupported order');this.ast.plan.order=direction;return this}
 page(size:number,after?:string){if(!Number.isInteger(size)||size<1||size>1000)throw Error('page size must be 1–1000');this.ast.page={size,...after?{after}:{}};return this}
 toAST():QueryAST{return structuredClone(this.ast)}
}
export type Completion={label:string;insert:string;from:number;to:number;detail:string;cursor?:number};
/** Completion tolerates incomplete syntax; it never submits a query. */
export function completeQuery(text:string,caret:number,fields:readonly QueryField[]):Completion[]{
 const before=text.slice(0,caret);const match=before.match(/[\w@-]*$/)!;let from=caret-match[0].length;const partial=match[0];const context=before.slice(0,from);
 let values:{label:string;insert:string;detail:string}[]=[];
 const add=(label:string,detail:string,insert=label)=>values.push({label,detail,insert});
 const fluent=/^\s*events\./.test(text);
 const where=context.match(/where(?:\s+|\(\s*)([\w@-]+|"[^"\\]+")\s*,?\s*(==|!=|>=|<=|>|<|contains)\s*,?\s*$/);
 if(/search(?:\s+|\(\s*)"(?:[^"\\]|\\.)*"\s*(?:,\s*|mode\s+)$/.test(context)){for(const mode of searchModes)add(mode,'Search Mode')}
 else if(where){const name=where[1].replace(/^"|"$/g,'');const f=fields.find(f=>f.name===name);for(const v of f?.values??[])add(v,'Mapped value',/^[A-Za-z_][\w-]*$/.test(v)&&!['true','false','null'].includes(v)?v:JSON.stringify(v));if(f?.type==='BOOL'){add('true','Boolean');add('false','Boolean')}add('null','Explicit null');if(f?.type==='STRING'&&!partial)add('Enter text…','Text value','""');}
 else if(/where(?:\s+|\(\s*)$/.test(context)){for(const f of fields)add(f.name,f.type,/^[A-Za-z_@][\w@-]*$/.test(f.name)?f.name:JSON.stringify(f.name))}
 else if(/where(?:\s+|\(\s*)(?:[\w@-]+|"[^"\\]+")\s*,?\s+$/.test(context)){for(const op of ['==','!=','>','>=','<','<=','contains'])add(op,'Comparison')}
 else if(/order(?:\s+|\(\s*)timestamp\s*,?\s+$/.test(context)){add('asc','Oldest first');add('desc','Newest first')}
 else if(/order(?:\s+|\(\s*)$/.test(context))add('timestamp','Time ordering');
 else if(/page(?:\s+|\(\s*)$/.test(context)){for(const n of ['50','100','250'])add(n,'Scroll fetch size')}
 else if(/(?:\||\.)\s*$/.test(context)){for(const stage of ['where','search','order','page'])add(stage,'Read-only stage',stage+(fluent?'(':' '))}
 else if(!context.trim()){add('events query','Pipeline query','events query | ');add('events.query()','Fluent query','events.query().')}
 return values.filter(v=>v.label.toLowerCase().startsWith(partial.toLowerCase())).slice(0,30).map(v=>({...v,from,to:caret,...v.detail==='Text value'?{cursor:1}:{}}));
}
export const events={query:()=>new EventQuery()};

export type QuerySyntax='builder'|'pipeline';
export function formatQuery(ast:QueryAST,syntax:QuerySyntax):string {
 const quoted=(value:string)=>JSON.stringify(value);
 const field=(name:string)=>/^[A-Za-z_@][\w@-]*$/.test(name)?name:quoted(name);
 const value=(v:Literal)=>v.kind==='string'?quoted(v.value):v.kind==='null'?'null':v.value;
 const fluent=syntax==='builder',stages:string[]=[];
 for(const f of ast.plan.filters)stages.push(fluent?`where(${field(f.field)}, ${f.op}, ${value(f.value)})`:`where ${field(f.field)} ${f.op} ${value(f.value)}`);
 if(ast.plan.search){const {text,mode}=ast.plan.search;stages.push(fluent?`search(${quoted(text)}${mode==='contains'?'':', '+mode})`:`search ${quoted(text)}${mode==='contains'?'':' mode '+mode}`)}
 if(ast.plan.order==='desc')stages.push(fluent?'order(timestamp, desc)':'order timestamp desc');
 if(ast.page)stages.push(fluent?`page(${ast.page.size}${ast.page.after?', '+quoted(ast.page.after):''})`:`page ${ast.page.size}${ast.page.after?' after '+quoted(ast.page.after):''}`);
 return (fluent?'events.query()':'events query')+stages.map(s=>fluent?'\n  .'+s:'\n  | '+s).join('');
}
/** Complete the next argument too, but never overwrite a following clause. */
export function guidedCompletion(text:string,c:Completion):Completion {
 const before=text.slice(0,c.from),tail=text.slice(c.to),fluent=/^\s*events\./.test(text);
 if(tail.trim()&&!/^\s*(?:\.\w+\(|\|)/.test(tail))return c;
 let suffix='';
 if(/where(?:\s+|\(\s*)$/.test(before))suffix=fluent?', ':' ';
 else if(c.detail==='Comparison')suffix=fluent?', ':' ';
 else if(c.detail==='English word'||c.detail==='Text value')return c;
 else if(['Mapped value','Definition value','Boolean','Explicit null','Newest first','Oldest first','Scroll fetch size','Search Mode'].includes(c.detail))suffix=fluent?')':'';
 return {...c,insert:c.insert+suffix};
}
