package main

import (
	"encoding/json"
	"io"
	"net/http"
)

func (b *bridge) diagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/__desktop/connection" && b.connection != nil {
		b.connection.serve(w, r, b.transport)
		return
	}
	if r.URL.Path == "/__desktop/connection" && r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"transport": b.transport})
		return
	}
	if !b.selfTest {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/__desktop/self-test.js":
		w.Header().Set("Content-Type", "text/javascript")
		if b.dashboardTest {
			io.WriteString(w, dashboardTestScript)
		} else {
			io.WriteString(w, selfTestScript)
		}
	case "/__desktop/report":
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", 405)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil || !json.Valid(raw) {
			http.Error(w, "Invalid Report", 400)
			return
		}
		select {
		case b.report <- raw:
		default:
		}
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

const selfTestScript = `(async()=>{
 const result={ok:false,userAgent:navigator.userAgent,checks:{},errors:[]};
 window.addEventListener('error',e=>result.errors.push(e.message));
 window.addEventListener('unhandledrejection',e=>result.errors.push(String(e.reason)));
 try{
  result.checks.secureContext=isSecureContext;result.checks.sharedWorker=typeof SharedWorker!=='undefined';result.checks.webLocks=!!navigator.locks;
  result.connection=await(await fetch('/__desktop/connection')).json();
  const caches=await fetch('/trunk',{headers:{Accept:'application/pil'}});result.checks.serverDirectory=caches.ok;if(!caches.ok)result.errors.push('Server directory: '+caches.status);
  const code="self.onmessage=async()=>{try{const root=await navigator.storage.getDirectory(),dir=await root.getDirectoryHandle('__snoot_self_test',{create:true}),f=await dir.getFileHandle('probe',{create:true}),h=await f.createSyncAccessHandle();const input=new Uint8Array([80,73,76,0,255]),out=new Uint8Array(5);h.write(input,{at:0});h.flush();h.read(out,{at:0});h.close();await root.removeEntry('__snoot_self_test',{recursive:true});if(out.some((v,i)=>v!==input[i]))throw Error('OPFS mismatch');await WebAssembly.compile(new Uint8Array([0,97,115,109,1,0,0,0]));self.postMessage({ok:true});}catch(e){self.postMessage({error:String(e)});}}";
  const url=URL.createObjectURL(new Blob([code],{type:'text/javascript'})),worker=new Worker(url);
  try{await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(Error('Storage worker timed out')),10000);worker.onmessage=({data})=>{clearTimeout(timer);data.ok?resolve():reject(Error(data.error));};worker.onerror=e=>{clearTimeout(timer);reject(Error(e.message));};worker.postMessage({});});result.checks.opfsSyncReadWrite=true;result.checks.wasm=true;}finally{worker.terminate();URL.revokeObjectURL(url);}
  const until=Date.now()+20000;while(Date.now()<until&&!document.body.innerText.includes('Search'))await new Promise(r=>setTimeout(r,100));
  result.checks.frontend=document.body.innerText.includes('Search');
  await new Promise(r=>setTimeout(r,2500));
  result.checks.keyboardHintRemoved=!document.querySelector('.query-key-guide');
  document.querySelector('.sidebar button[title="Profile"]')?.click();
  const profileDeadline=Date.now()+5000;
  while(Date.now()<profileDeadline&&!document.getElementById('profile-connections-tab'))await new Promise(r=>setTimeout(r,50));
  document.getElementById('profile-connections-tab')?.click();
  const connectionDeadline=Date.now()+5000;
  while(Date.now()<connectionDeadline&&!document.querySelector('.desktop-connection input'))await new Promise(r=>setTimeout(r,50));
  result.checks.connectionVisible=document.querySelector('.desktop-connection input')?.value.startsWith(result.connection.transport)===true;
  document.getElementById('profile-storage-tab')?.click();
  const storageDeadline=Date.now()+10000;
  while(Date.now()<storageDeadline&&!document.querySelector('.storage-totals'))await new Promise(r=>setTimeout(r,50));
  result.checks.storageVisible=document.getElementById('profile-storage-tab')?.getAttribute('aria-selected')==='true'&&!!document.querySelector('.storage-totals');
  result.checks.storageClearAvailable=Array.from(document.querySelectorAll('.storage-panel button')).some(b=>b.textContent==='Clear All Local Caches');
  // Exercise the real Storage UI against a fixture in the isolated self-test origin.
  const resetKey='snoot-self-test-clear',pendingReset=sessionStorage.getItem(resetKey),storageRoot=await navigator.storage.getDirectory(),replicas=await storageRoot.getDirectoryHandle('snoot-store',{create:true});
  if(!pendingReset){
   const namespace=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode('self-test-'+crypto.randomUUID()))),n=>n.toString(16).padStart(2,'0')).join('');
   // Reproduce old WebKit plus metadata left by an incompatible prior format.
   Object.defineProperty(FileSystemFileHandle.prototype,'createWritable',{value:undefined,configurable:true});
   const seedCode="self.onmessage=async({data})=>{try{const root=await navigator.storage.getDirectory(),stores=await root.getDirectoryHandle('snoot-store',{create:true}),dir=await stores.getDirectoryHandle(data,{create:true});for(const name of ['probe.bin','commit.pil']){const f=await dir.getFileHandle(name,{create:true}),h=await f.createSyncAccessHandle();h.write(new Uint8Array([255]),{at:0});h.truncate(1);h.flush();h.close()}self.postMessage({ok:true})}catch(e){self.postMessage({error:String(e)})}}";
   const seedURL=URL.createObjectURL(new Blob([seedCode],{type:'text/javascript'})),seedWorker=new Worker(seedURL);
   try{await new Promise((resolve,reject)=>{seedWorker.onmessage=({data})=>data.ok?resolve():reject(Error(data.error));seedWorker.onerror=e=>reject(Error(e.message));seedWorker.postMessage(namespace)})}finally{seedWorker.terminate();URL.revokeObjectURL(seedURL)}
   Array.from(document.querySelectorAll('.storage-panel button')).find(b=>b.textContent==='Refresh')?.click();
   const deadline=Date.now()+5000;
   while(Date.now()<deadline&&!document.querySelector('[data-storage-id="'+namespace+'"] .storage-clear'))await new Promise(r=>setTimeout(r,50));
   const clear=document.querySelector('[data-storage-id="'+namespace+'"] .storage-clear');if(!clear)throw Error('Deletion fixture missing from Storage');clear.click();
   await new Promise(r=>setTimeout(r,50));
   const confirmation=document.querySelector('.storage-confirm');if(!confirmation||document.activeElement!==confirmation)throw Error('Deletion confirmation not focused');
   sessionStorage.setItem(resetKey,namespace);
   Array.from(confirmation.querySelectorAll('button')).find(b=>b.textContent==='Clear Local Copy').click();
   await new Promise(r=>setTimeout(r,2000));throw Error(document.querySelector('.storage-panel [role="alert"]')?.textContent||'Storage deletion did not reload');
  }
  const cleared=await replicas.getDirectoryHandle(pendingReset);let probeRemains=false;try{await cleared.getFileHandle('probe.bin');probeRemains=true}catch(e){if(e.name!=='NotFoundError')throw e}
  result.checks.storageDeletionWithoutWritable=!probeRemains&&!document.querySelector('[data-storage-id="'+pendingReset+'"]');
  if(result.checks.storageDeletionWithoutWritable){await replicas.removeEntry(pendingReset,{recursive:true});sessionStorage.removeItem(resetKey)}
  document.querySelector('dialog[open] button[aria-label="Close Dialog"]')?.click();
  const frame=document.querySelector('.workspace'),bar=document.querySelector('.topbar'),pane=document.querySelector('.page-scroll'),content=document.querySelector('.page-content');
  if(!frame||!bar||!pane||!content)throw Error('Missing workspace scroll layout');
  const spacer=document.createElement('div');spacer.style.height='1500px';content.append(spacer);
  pane.scrollTop=0;await new Promise(r=>setTimeout(r,50));
  const geometry=el=>{const r=el.getBoundingClientRect();return [r.x,r.y,r.width,r.height];};
  const before={frame:geometry(frame),bar:geometry(bar),content:geometry(content)};pane.scrollTop=400;
  await new Promise(r=>setTimeout(r,50));
  result.checks.fixedFrame=JSON.stringify(before.frame)===JSON.stringify(geometry(frame));
  result.checks.fixedHeader=JSON.stringify(before.bar)===JSON.stringify(geometry(bar));
  result.checks.contentScroll=pane.scrollTop===400&&geometry(content)[1]===before.content[1]-400&&document.scrollingElement.scrollTop===0&&document.scrollingElement.scrollHeight<=innerHeight;
  pane.scrollTop=0;spacer.remove();
  result.checks.profileOnlyIDB=(await indexedDB.databases()).every(d=>d.name==='snoot-profile');
  result.checks.noHorizontalOverflow=document.documentElement.scrollWidth<=innerWidth;
  result.ok=Object.values(result.checks).every(Boolean)&&result.errors.length===0;
 }catch(e){result.errors.push(String(e));}
 await fetch('/__desktop/report',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(result)});
})();`

const dashboardTestScript = `(async()=>{
 const result={ok:false,checks:{},errors:[],scripts:[],workerAssets:[],dashboard:''};
 try{
  const wait=async(check,ms)=>{const until=Date.now()+ms;while(Date.now()<until){if(check())return;await new Promise(r=>setTimeout(r,100))}throw Error('Dashboard check timed out')};
  await wait(()=>Array.from(document.querySelectorAll('nav button')).some(b=>b.textContent==='Dashboards'),15000);
  Array.from(document.querySelectorAll('nav button')).find(b=>b.textContent==='Dashboards').click();
  await wait(()=>document.querySelector('.fast-grid')||document.querySelector('.subscription-empty')||document.querySelector('.fast-dashboard [role="alert"]'),35000);
  result.dashboard=(document.querySelector('.fast-dashboard')||document.querySelector('.subscription-empty'))?.innerText.slice(0,1800)||'';
  result.scripts=Array.from(document.scripts).map(s=>s.src).filter(Boolean);
  result.workerAssets=performance.getEntriesByType('resource').map(r=>r.name).filter(n=>/worker|wasm/.test(n));
  result.errors=Array.from(document.querySelectorAll('[role="alert"]')).map(e=>e.textContent);
  result.checks.chartsRendered=!!document.querySelector('.fast-grid');
  result.checks.needsSubscription=!!document.querySelector('.subscription-empty');
  result.ok=!result.errors.length&&(result.checks.chartsRendered||result.checks.needsSubscription);
 }catch(e){result.errors.push(String(e));result.dashboard=document.body.innerText.slice(-1800)}
 await fetch('/__desktop/report',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(result)});
})();`
