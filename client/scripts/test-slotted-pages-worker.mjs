function compare(a,b){for(let i=0;i<16;i++){if(a[i]!==b[i])return a[i]-b[i];}return 0;}
function crc32c(bytes){let crc=0xffffffff;for(let i=0;i<bytes.length;i++){crc^=i>=24&&i<28?0:bytes[i];for(let j=0;j<8;j++)crc=(crc>>>1)^((crc&1)?0x82f63b78:0);}return (crc^0xffffffff)>>>0;}
function find(p,key){const at=i=>(p[37+i]&127)*32,start=i=>(p[37+i]&128)===0;let lo=0,hi=p[28],right=true;
  while(lo<hi){const mid=lo+Math.floor((hi-lo)/2);let found=-1;
    const scan=(from,to,step)=>{for(let i=from;step>0?i<to:i>=to;i+=step){if(start(i)){found=i;break;}}};
    if(right){scan(mid,hi,1);if(found<0)scan(mid-1,lo,-1);}else{scan(mid,lo,-1);if(found<0)scan(mid+1,hi,1);}
    if(found<0)return -1;const cmp=compare(p.subarray(at(found),at(found)+16),key);if(cmp===0)return found;
    if(cmp<0)lo=found+1;else hi=found;right=!right;
  }return -1;
}
function history(p,i){if(i<0)return [];const d=new DataView(p.buffer,p.byteOffset,p.byteLength),at=i=>(p[37+i]&127)*32;
  let end=i+1;while(end<p[28]&&(p[37+end]&128)!==0)end++;const capacity=1+(end-i-1)*4,base=d.getUint16(at(i)+16,true),head=d.getUint16(at(i)+18,true),out=[];
  for(let delta=0;delta<capacity&&base+delta<=65535;delta++){const n=(head+delta)%capacity;const pos=n===0?at(i)+24:at(i+1+Math.floor((n-1)/4))+((n-1)%4)*8;const ref=d.getBigUint64(pos,true);if(ref!==0n)out.push([base+delta,ref.toString()]);}return out;
}
self.onmessage=async({data:{action}})=>{try{
  const root=await navigator.storage.getDirectory();const handle=await root.getFileHandle('slotted.idx',{create:action==='write'});const file=await handle.createSyncAccessHandle();
  if(action==='write'){const bytes=new Uint8Array(await(await fetch('/pages.bin')).arrayBuffer());if(file.write(bytes,{at:0})!==bytes.length)throw Error('Short write');file.truncate(bytes.length);file.flush();file.close();self.postMessage({written:bytes.length});return;}
  const queries=await(await fetch('/queries.json')).json(),size=file.getSize(),fences=[];const p=new Uint8Array(4096);let extensions=0;
  for(let off=0;off<size;off+=4096){if(file.read(p,{at:off})!==4096)throw Error('Short read');if(crc32c(p)!==new DataView(p.buffer).getUint32(24,true))throw Error('Checksum');const at=(p[37]&127)*32;fences.push({first:p.slice(at,at+16),off});for(let i=0;i<p[28];i++)if(p[37+i]&128)extensions++;}
  let references=0;const begin=performance.now();
  for(const q of queries){const key=Uint8Array.from(q.key.match(/../g),b=>parseInt(b,16));let lo=0,hi=fences.length;while(lo<hi){const mid=(lo+hi)>>>1;if(compare(fences[mid].first,key)<=0)lo=mid+1;else hi=mid;}
    let got=[];if(lo>0){file.read(p,{at:fences[lo-1].off});got=history(p,find(p,key));}if(JSON.stringify(got)!==JSON.stringify(q.history))throw Error('History mismatch '+q.key);references+=got.length;
  }file.close();self.postMessage({pages:fences.length,queries:queries.length,references,extensions,elapsed_ms:performance.now()-begin,bytes:size});
}catch(e){self.postMessage({error:e.stack??String(e)});}};
