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
		io.WriteString(w, selfTestScript)
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
  const caches=await fetch('/caches',{headers:{Accept:'application/json'}});if(!caches.ok)throw Error('Server directory: '+caches.status);result.checks.serverDirectory=true;
  const code="self.onmessage=async()=>{try{const root=await navigator.storage.getDirectory(),dir=await root.getDirectoryHandle('__snoot_self_test',{create:true}),f=await dir.getFileHandle('probe',{create:true}),h=await f.createSyncAccessHandle();const input=new Uint8Array([80,73,76,0,255]),out=new Uint8Array(5);h.write(input,{at:0});h.flush();h.read(out,{at:0});h.close();await root.removeEntry('__snoot_self_test',{recursive:true});if(out.some((v,i)=>v!==input[i]))throw Error('OPFS mismatch');await WebAssembly.compile(new Uint8Array([0,97,115,109,1,0,0,0]));self.postMessage({ok:true});}catch(e){self.postMessage({error:String(e)});}}";
  const url=URL.createObjectURL(new Blob([code],{type:'text/javascript'})),worker=new Worker(url);
  try{await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(Error('Storage worker timed out')),10000);worker.onmessage=({data})=>{clearTimeout(timer);data.ok?resolve():reject(Error(data.error));};worker.onerror=e=>{clearTimeout(timer);reject(Error(e.message));};worker.postMessage({});});result.checks.opfsSyncReadWrite=true;result.checks.wasm=true;}finally{worker.terminate();URL.revokeObjectURL(url);}
  const until=Date.now()+20000;while(Date.now()<until&&!document.body.innerText.includes('Search'))await new Promise(r=>setTimeout(r,100));
  result.checks.frontend=document.body.innerText.includes('Search');
  await new Promise(r=>setTimeout(r,2500));
  result.checks.keyboardHintRemoved=!document.querySelector('.query-key-guide');
  document.querySelector('.sidebar button[title="Connection"]')?.click();
  const connectionDeadline=Date.now()+5000;
  while(Date.now()<connectionDeadline&&!document.querySelector('.desktop-connection input'))await new Promise(r=>setTimeout(r,50));
  result.checks.connectionVisible=document.querySelector('.desktop-connection input')?.value.startsWith(result.connection.transport)===true;
  document.querySelector('dialog[open] button[aria-label="Close Dialog"]')?.click();
  const frame=document.querySelector('.workspace'),bar=document.querySelector('.topbar'),pane=document.querySelector('.page-scroll'),content=document.querySelector('.page-content');
  if(!frame||!bar||!pane||!content)throw Error('Missing workspace scroll layout');
  const spacer=document.createElement('div');spacer.style.height='1500px';content.append(spacer);
  const geometry=el=>{const r=el.getBoundingClientRect();return [r.x,r.y,r.width,r.height];};
  const before={frame:geometry(frame),bar:geometry(bar),content:geometry(content)};pane.scrollTop=400;
  await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
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
