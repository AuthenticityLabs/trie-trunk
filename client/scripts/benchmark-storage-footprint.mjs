// Compare one live synthetic cache with its fully expanded JSONL representation.
// No timing claims: response decoding/JSON construction are outside this size test.
import {readFile,mkdir,writeFile,readdir,stat} from 'node:fs/promises';
import {resolve,join,extname,relative} from 'node:path';
import {createHash} from 'node:crypto';
import {gzipSync,brotliCompressSync,constants} from 'node:zlib';
import {Codec} from '../services/pil/wasm.ts';
import {PILStream,consume} from '../services/pil/stream.ts';
import {compile} from '../services/pil/metadata.ts';
import {PILEvent} from '../services/pil/event.ts';
import {expandEvent,jsonText} from '../services/dictionary.ts';
const [rootArg,outputArg,origin='http://127.0.0.1:3020']=process.argv.slice(2);
if(!rootArg||!outputArg)throw Error('usage: benchmark-storage-footprint.mjs <cache-root> <output-directory> [origin]');
const root=resolve(rootArg),out=resolve(outputArg);
const manifest=JSON.parse(await readFile(join(root,'demo-data.json'),'utf8'));
if(manifest.Synthetic!==true)throw Error('This comparison requires the synthetic demo manifest');
const codec=new Codec(await WebAssembly.instantiate(await WebAssembly.compile(await readFile(new URL('../assets/pil.wasm',import.meta.url)))));
const definitions=new Map(),mappings=new Map(),lines=[],bare=[],ids=new Set();let events=0;
const query=new URLSearchParams({tap:`${manifest.Cache}/*/*/*`,from:manifest.From,to:manifest.To,limit:'all'});
const response=await fetch(`${origin}/collect?${query}`,{headers:{Accept:'application/pil','PIL-Version':'2','PIL-Features':'adaptive-integers','Trunk-Completion':'body'},signal:AbortSignal.timeout(120000)});
const parser=new PILStream(codec,definitions,()=>{},binding=>{
 mappings.set(binding.stream,{mapping:compile(definitions.get(binding.digest),binding.tap,[]),tap:binding.tap});
},async batch=>{
 for(let i=0;i<batch.rows.length;i+=3){
  const {mapping,tap}=mappings.get(batch.rows[i]);
  const event=new PILEvent(new Uint8Array(batch.bytes,batch.rows[i+1],batch.rows[i+2]-batch.rows[i+1]),mapping,batch.common?.[i/3]);
  const object=expandEvent(event),key=`${tap}:${object.event_id}`;
  if(ids.has(key))throw Error('Duplicate source/event identity');ids.add(key);
  const payload={...object};if(!mapping.definition.fields.some(f=>f.name==='time'))delete payload.time;
  bare.push(jsonText(payload)+'\n');
  const [cache,service,resource,branch]=tap.split('/');
  lines.push(jsonText({cache,service,resource,branch,...object})+'\n');events++;
 }
});
await consume(response,parser,AbortSignal.timeout(120000));
if(events!==manifest.Events)throw Error(`Expected ${manifest.Events} events, received ${events}`);
const files=[],groups={};
async function walk(dir){for(const entry of await readdir(dir,{withFileTypes:true})){
 const path=join(dir,entry.name);if(entry.isDirectory())await walk(path);else if(entry.isFile()){
  const s=await stat(path),name=relative(root,path),ext=extname(entry.name)||'(other)';
  const group=entry.name.startsWith('collection.')?'optional collection index/state':ext;
  files.push({name,bytes:s.size,allocated_bytes:s.blocks*512});
  groups[group]=(groups[group]??0)+s.size;
 }
}}
await walk(join(root,manifest.Cache));
const raw=Buffer.from(lines.join('')),payload=Buffer.from(bare.join(''));
const jsonlGzip=gzipSync(raw,{level:6}),jsonlBrotli=brotliCompressSync(raw,{params:{[constants.BROTLI_PARAM_QUALITY]:6}});
const total=files.reduce((n,f)=>n+f.bytes,0),allocated=files.reduce((n,f)=>n+f.allocated_bytes,0);
const report={generated_at:new Date().toISOString(),events,from:manifest.From,to:manifest.To,
 expanded_jsonl_bytes:raw.length,payload_only_jsonl_bytes:payload.length,jsonl_gzip6_bytes:jsonlGzip.length,jsonl_brotli6_bytes:jsonlBrotli.length,
 trie_trunk_bytes:total,trie_trunk_allocated_bytes:allocated,file_count:files.length,by_component:groups,
 savings_vs_expanded_percent:100*(1-total/raw.length),savings_vs_payload_only_percent:100*(1-total/payload.length),
 expanded_jsonl_sha256:createHash('sha256').update(raw).digest('hex'),files,
 notes:['Expanded JSONL contains all named fields, expanded dictionary values, textual UUIDs, derived ISO time, and cache/service/resource/branch context.',
 'Payload-only JSONL omits derived time and catalog context, so trie-trunk is not credited solely for moving those into metadata.',
 'Trie-trunk total includes every regular file inside the demo cache: optional collection index/state, aggregates, maps, tries, PAC directories, staging, configuration, and payloads. Demo-generation manifest is excluded.',
 'Whole-file gzip6/Brotli6 JSONL are size references only, without equivalent indexes, aggregates, or independent hourly access.',
 'No search, ingestion, or transport latency claim is made by this footprint comparison.']};
await mkdir(out,{recursive:true});
await Promise.all([writeFile(join(out,'footprint.json'),JSON.stringify(report,null,2)+'\n'),writeFile(join(out,'expanded.jsonl'),raw),writeFile(join(out,'expanded.jsonl.gz'),jsonlGzip),writeFile(join(out,'expanded.jsonl.br'),jsonlBrotli)]);
console.log(JSON.stringify({...report,files:undefined},null,2));
