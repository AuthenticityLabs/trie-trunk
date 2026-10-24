import {readFileSync,writeFileSync} from 'node:fs';
import {gzipSync,gunzipSync,brotliCompressSync,brotliDecompressSync,constants} from 'node:zlib';
import {performance} from 'node:perf_hooks';
const body=readFileSync(process.argv[2] ?? '/tmp/trie-trunk-dense-summary.bin');
const out=[];
for(const [codec,enc,dec] of [['gzip6',x=>gzipSync(x,{level:6}),gunzipSync],['gzip9',x=>gzipSync(x,{level:9}),gunzipSync],['brotli11',x=>brotliCompressSync(x,{params:{[constants.BROTLI_PARAM_QUALITY]:11}}),brotliDecompressSync]]){
 let packed,t=performance.now();for(let i=0;i<3;i++)packed=enc(body);const encodeMs=(performance.now()-t)/3;
 t=performance.now();for(let i=0;i<50;i++){if(!dec(packed).equals(body))throw Error('roundtrip');}const decodeMs=(performance.now()-t)/50;
 out.push({codec,bodyBytes:body.length,packetBytes:packed.length+32,encodeMs,decodeMs});
}
writeFileSync(new URL('compression-dense-node.json',import.meta.url),JSON.stringify({runtime:process.version,note:'Node native codecs; 240k-event synthetic day, not browser measurements.',results:out},null,2)+'\n');console.log(out);
