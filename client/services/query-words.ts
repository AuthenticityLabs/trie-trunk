import type {Completion,QueryField} from './query.ts';
let dictionary:Promise<readonly string[]>|undefined;
/** A separate static chunk, requested only when completing a free-text word. */
export function loadEnglishWords():Promise<readonly string[]>{
 return dictionary??=import('an-array-of-english-words/index.json?raw').then(m=>(JSON.parse(m.default) as string[]).sort()).catch(e=>{dictionary=undefined;throw e});
}
export function prefixWords(words:readonly string[],prefix:string,limit=8):string[]{
 if(!/^[a-z]{2,}$/i.test(prefix))return [];
 const lower=prefix.toLowerCase();let lo=0,hi=words.length;
 while(lo<hi){const mid=(lo+hi)>>>1;if(words[mid]<lower)lo=mid+1;else hi=mid}
 const results:string[]=[];
 for(let i=lo;i<words.length&&results.length<256&&words[i].startsWith(lower);i++){
  const word=prefix===prefix.toUpperCase()?words[i].toUpperCase():/^[A-Z]/.test(prefix)?words[i][0].toUpperCase()+words[i].slice(1):words[i];
  if(word!==prefix)results.push(word);
 }
 return results.sort((a,b)=>a.length-b.length||a.localeCompare(b)).slice(0,limit);
}
/** Only quoted values/search text; never complete identifiers, cursors or numeric literals. */
export function textCompletionContext(text:string,caret:number,fields:readonly QueryField[]){
 let quote=-1,escaped=false;
 for(let i=0;i<caret;i++){const c=text[i];if(quote>=0&&escaped){escaped=false;continue}if(quote>=0&&c==='\\'){escaped=true;continue}if(c==='"')quote=quote<0?i:-1}
 if(quote<0||escaped)return;
 const before=text.slice(0,quote),where=before.match(/where(?:\s+|\(\s*)([\w@-]+|"[^"\\]+")\s*,?\s*(?:==|!=|>=|<=|>|<|contains)\s*,?\s*$/);
 const field=where?fields.find(f=>f.name===where[1].replace(/^"|"$/g,'')):undefined;
 if(where&&field?.type!=='STRING')return;
 if(!where&&before.trim()&&!/search(?:\s+|\(\s*)$/.test(before))return;
 let typed:string;try{typed=JSON.parse('"'+text.slice(quote+1,caret)+'"')}catch{return}
 let end=caret,slash=false;
 for(;end<text.length;end++){if(slash){slash=false;continue}if(text[end]==='\\'){slash=true;continue}if(text[end]==='"'){end++;break}}
 // If the literal is not closed, replace only what has been typed.
 if(end===text.length&&text[end-1]!=='"')end=caret;
 const word=text.slice(quote+1,caret).match(/[A-Za-z]{2,}$/)?.[0];
 return {quote,end,typed,field,word,wordStart:word?caret-word.length:caret,wordEnd:caret+(text.slice(caret).match(/^[A-Za-z]*/)?.[0].length??0)};
}
export function valueCompletions(text:string,caret:number,fields:readonly QueryField[],words:readonly string[]=[]):Completion[]{
 const c=textCompletionContext(text,caret,fields);if(!c)return [];
 if(c.field?.values.length)return c.field.values.filter(v=>v.toLowerCase().startsWith(c.typed.toLowerCase())).slice(0,30).map(v=>({label:v,detail:'Definition value',insert:JSON.stringify(v),from:c.quote,to:c.end}));
 if(!c.word)return [];
 return prefixWords(words,c.word).map(word=>({label:word,detail:'English word',insert:word,from:c.wordStart,to:c.wordEnd}));
}
