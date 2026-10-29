import {readFileSync,writeFileSync} from 'node:fs';
import {pathToFileURL} from 'node:url';
const [root,input,out]=process.argv.slice(2),load=p=>import(pathToFileURL(root+'/'+p));
const {Codec}=await load('client/services/pil/wasm.ts'),{PILStream}=await load('client/services/pil/stream.ts'),{pacHeader}=await load('client/services/pil/pac.ts'),{nativePAC}=await load('client/services/pil/pac-native.ts'),{span}=await load('client/services/pil/layout.ts');
const codec=new Codec((await WebAssembly.instantiate(readFileSync(root+'/client/assets/pil.wasm'))).instance),defs=new Map();
const audit={hours:0,events:0,indexChunkBytes:0,pacHeaderBytes:0,directoryBytes:0,commonBytes:0,memberBytes:0,maskBytes:0,fields:{},memberWidths:{},definitions:[]};
const parser=new PILStream(codec,defs,()=>{},()=>{},async()=>{throw Error('Expanded event')},async r=>{
 if(r.kind==='chunk'&&r.file==='index')audit.indexChunkBytes+=r.bytes.length;
 if(r.kind!=='pac')return;
 const p=pacHeader(r.bytes,0,0xffffffff,new Set([1,nativePAC])),def=defs.get(r.binding.digest).definition;
 if(!audit.definitions.length)audit.definitions.push(def);
 audit.hours++;audit.events+=p.count;audit.pacHeaderBytes+=p.start;audit.directoryBytes+=p.directory?.length??0;audit.commonBytes+=p.common.length;audit.memberBytes+=p.end-p.start;
 for(let j=0;j<p.count;j++){
  const raw=r.bytes.subarray(r.bounds[j*2],r.bounds[j*2+1]);audit.maskBytes+=Math.ceil(def.fields.length/4);audit.memberWidths[raw.length]=(audit.memberWidths[raw.length]??0)+1;
  for(let i=0;i<def.fields.length;i++){
   const name=def.fields[i].name,v=span(raw,def,i),c=p.common.length?span(p.common,def,i):{state:0};
   const f=audit.fields[name]??={memberPresent:0,memberValueBytes:0,memberOffsetEntries:0,commonPresent:0};
   if(v.state>=2){f.memberPresent++;f.memberValueBytes+=v.end-v.start;if(v.state===3)f.memberOffsetEntries++}
   if(c.state>=2)f.commonPresent++;
  }
 }
});
const wire=readFileSync(input);try{for(let i=0;i<wire.length;i+=65536)await parser.feed(wire.subarray(i,i+65536));parser.finish()}finally{parser.close()}
writeFileSync(out,JSON.stringify(audit,null,2)+'\n');console.log(JSON.stringify(audit,null,2));
