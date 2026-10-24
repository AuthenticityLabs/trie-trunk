// Isolated OPFS allocation experiment. Synthetic bytes, not a codec benchmark.
function bytes(c,slot){
 const n=c.payload-slot%17,b=new Uint8Array(n+4);new DataView(b.buffer).setUint32(0,n,true);
 for(let i=0;i<n;i++)b[i+4]=(slot*17+i*29)%256;return b;
}
function entries(c){
 if(!c.objects)return c.groups.flatMap(([start,count])=>Array.from({length:count},(_,i)=>({at:(start+i)*c.stride,b:bytes(c,start+i)})));
 const out=[];for(let obj=c.objects-1;obj>=0;obj--){
  const base=obj*c.stride,id=new Uint8Array(16);new DataView(id.buffer).setUint32(12,obj+1);
  out.push({at:base,b:id});for(const rev of c.revisions??Array.from({length:c.revisionCount},(_,i)=>i)){
   if(rev<0||rev>65535)throw Error('revision overflow');
   const b=new Uint8Array(8),v=new DataView(b.buffer);v.setUint32(0,obj+1,true);v.setUint32(4,rev+1,true);
   out.push({at:base+64+rev*8,b});
  }
 }return out;
}
const check=(value,message)=>{if(!value)throw Error(message)};
const write=(f,b,at)=>{let n=0;while(n<b.length){const k=f.write(b.subarray(n),{at:at+n});check(k>0,'short write');n+=k}};
const read=(f,at,n)=>{const b=new Uint8Array(n);check(f.read(b,{at})===n,'short read');return b};
const usage=async()=>{const e=await navigator.storage.estimate();return {usage:e.usage,quota:e.quota,details:e.usageDetails}};
onmessage=async({data:{c,action}})=>{
 try{
 const root=await navigator.storage.getDirectory();
 if(action==='prepare'||action==='delete')try{await root.removeEntry('fixed-slot-probe',{recursive:true})}catch(e){if(e.name!=='NotFoundError')throw e}
 if(action==='delete'){postMessage({result:true});return}
 const dir=await root.getDirectoryHandle('fixed-slot-probe',{create:true});
 const file=await dir.getFileHandle('slots',{create:true});let f=await file.createSyncAccessHandle();
 const used=entries(c);
 if(action==='prepare'){
  const before=await usage(),start=performance.now();
  if(c.zero){const zero=new Uint8Array(1<<20);for(let at=0;at<c.slots*c.stride;at+=zero.length)write(f,zero,at)}
  else if(!c.seek)f.truncate(c.slots*c.stride);
  f.flush();f.close();const reserved=await usage();f=await file.createSyncAccessHandle();
  for(const {at,b} of used){check(b.length<=c.stride,'slot overflow');write(f,b,at)}
  f.flush();const writeFlushMs=performance.now()-start,logical=f.getSize();f.close();const closed=await usage();
  postMessage({result:{case:c,before,reserved,closed,logical,writeFlushMs,records:used.length,dataBytes:used.reduce((n,e)=>n+e.b.length,0)}});return;
 }
 for(const {at,b} of used){const got=read(f,at,b.length);check(got.every((v,i)=>v===b[i]),'record changed')}
 if(c.objects)for(let obj=0;obj<c.objects;obj++)check(read(f,obj*c.stride+16,48).every(v=>v===0),'header padding changed');
 const begin=performance.now();for(let i=0;i<2000;i++){const {at,b}=used[(i*997)%used.length],got=read(f,at,4);check(got.every((v,j)=>v===b[j]),'header changed')}
 const headerReadUs=(performance.now()-begin)*1000/2000;
 f.close();const reopened=await usage();
 postMessage({result:{verified:used.length,reopened,headerReadUs}});
 }catch(e){postMessage({error:e.stack||String(e)})}
};
