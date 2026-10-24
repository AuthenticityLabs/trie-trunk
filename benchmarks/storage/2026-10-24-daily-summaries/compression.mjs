import { readFileSync, writeFileSync } from 'node:fs';
import { gzipSync, gunzipSync, brotliCompressSync, brotliDecompressSync, constants } from 'node:zlib';
import { performance } from 'node:perf_hooks';
const results=[];
for(let sample=1;sample<=3;sample++) {
 const original=readFileSync(new URL(`sample-${sample}.sumday`,import.meta.url));
 const body=gunzipSync(original.subarray(32));
 for(const [codec,encode,decode] of [
  ['gzip6',x=>gzipSync(x,{level:6}),gunzipSync],
  ['gzip9',x=>gzipSync(x,{level:9}),gunzipSync],
  ['brotli11',x=>brotliCompressSync(x,{params:{[constants.BROTLI_PARAM_QUALITY]:11}}),brotliDecompressSync],
 ]) {
  const packed=encode(body);
  if(!decode(packed).equals(body)) throw Error('roundtrip');
  const measure=(fn,n)=>{for(let i=0;i<3;i++) fn();const t=performance.now();for(let i=0;i<n;i++) fn();return (performance.now()-t)/n;};
  results.push({sample,codec,decodedBytes:body.length,packetBytes:packed.length+32,encodeMs:measure(()=>encode(body),30),decodeMs:measure(()=>decode(packed),1000)});
 }
 // Native Web API gzip decoding path; Node implementation, not a browser benchmark.
 const packet=gzipSync(body,{level:9});
 let elapsed=0;
 for(let i=0;i<30;i++) {const t=performance.now();const data=await new Response(new Blob([packet]).stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer();elapsed+=performance.now()-t;if(data.byteLength!==body.length)throw Error('web decode');}
 results.push({sample,codec:'gzip9-DecompressionStream-Node',decodeMs:elapsed/30});
}
writeFileSync(new URL('compression-node.json',import.meta.url),JSON.stringify({runtime:process.version,note:'Native Node codec timings, not browser or Go timings. 32-byte envelope included in packet bytes.',results},null,2)+'\n');
console.log(JSON.stringify(results,null,2));
