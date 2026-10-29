// Isolated full-app test: temporary build/profile and loopback fixture server.
// No running trie-trunk instance or user browser storage is accessed.
import assert from "node:assert/strict";
import { readFile, mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "vite";
import { bytes, frame, magic, le, pilObject } from "../services/pil-fixtures.mjs";
import { Codec } from "../services/pil/wasm.ts";
import { PILStream } from "../services/pil/stream.ts";
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright-core");
const root = fileURLToPath(new URL("../../", import.meta.url));
const output = await mkdtemp(join(tmpdir(), "snoot-search-test-"));
const wire = new Uint8Array(await readFile(root + "client/services/fixtures/native-ranges/ranges.pil"));
const wasm = await WebAssembly.compile(await readFile(root + "client/assets/pil.wasm"));
const definitions = new Map(), records = [];
const parser = new PILStream(new Codec(await WebAssembly.instantiate(wasm)),new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{},async r=>records.push(r));
await parser.feed(wire);parser.finish();parser.close();
const first=records[0],binding=first.binding,definition=definitions.get(binding.digest),hash=Uint8Array.from(Buffer.from(binding.digest,"hex"));
const define=bytes([1],frame([3,3,3],[hash,definition.store,definition.pil]));
const bind=bytes([2],frame([2,3,3],[le(binding.stream,4),hash,new TextEncoder().encode(binding.tap)]));
const schemas=bytes(magic,define,bind,[255,254]);
const state=records.find(r=>r.kind==="chunk"&&r.file==="state");
const warm=bytes(magic,bind,[4],frame([2,3],[le(binding.stream,4),pilObject({...first.manifest,IndexBases:[]})]),[5],frame([2,2,2,3,3],[le(binding.stream,4),le(2,1),le(0,8),Uint8Array.from(Buffer.from(state.digest,"hex")),state.bytes]),[6],frame([2],[le(binding.stream,4)]),[255,254]);
const requests=[],issues=[];
const historyWire=await readFile(root+"client/services/fixtures/replay/history.pil");
const historyMetadata=JSON.parse(await readFile(root+"client/services/fixtures/replay/metadata.json","utf8"));
const rejectionWire=await readFile(root+"client/services/fixtures/rejections/inspection.jsonl","utf8");
const historyRequests=[],auditRequests=[];
let stateChecks=0,auditTruncated=false;
let browser,server;
try {
 await build({root:root+"client",configFile:root+"client/vite.config.ts",logLevel:"warn",build:{outDir:output,emptyOutDir:true}});
 server=createServer(async(req,res)=>{
  const url=new URL(req.url,"http://localhost");
  const headers={"Content-Type":"application/pil","PIL-Version":"2","Definition-Version":binding.digest,"Trunk-Completion":"body","Trunk-Features":"native-ranges"};
  if(url.pathname==="/caches"){
   res.writeHead(200,{"Content-Type":"application/json"});res.end(JSON.stringify({caches:[binding.tap]}));return;
  }
  if(url.pathname==="/schemas"){res.writeHead(200,headers);res.end(schemas);return;}
  if(url.pathname==="/objects/history"){
   historyRequests.push(url);
   if(url.searchParams.get("id")==="disabled"){res.writeHead(409);res.end("object history requires collection.idx");return;}
   res.writeHead(200,{...headers,"Definition-Version":historyMetadata.definition,"Trunk-State":historyMetadata.token,"Trunk-Replay":"retained-history"});
   res.end(historyWire);return;
  }
  if(url.pathname==="/objects/state"){
   stateChecks++;
   const current=stateChecks===1;
   res.writeHead(200,{"Content-Type":"application/json"});res.end(JSON.stringify({token:current?historyMetadata.token:"a".repeat(64),current}));return;
  }
  if(url.pathname==="/rejections"){
   if(url.searchParams.get("format")==="availability"){res.writeHead(200,{"Content-Type":"application/json"});res.end(JSON.stringify({available:true}));return;}
   auditRequests.push(url);
   res.writeHead(200,{"Content-Type":"application/x-ndjson","Trunk-Completion":"body"});
   res.end(auditTruncated?rejectionWire.split("\n")[0]+"\n":rejectionWire);return;
  }
  if(url.pathname==="/collect"){
   requests.push({url,headers:req.headers});
   if(url.searchParams.get("format")!=="native"){
    issues.push("unexpected expanded fallback");res.writeHead(500);res.end("Expected native request");return;
   }
   res.writeHead(200,{...headers,"Trunk-Format":"native-ranges/1"});
   res.end(req.headers["trunk-coverage"]?warm:wire);return;
  }
  try {
   const name=url.pathname==="/"?"/index.html":url.pathname;
   if(name.includes(".."))throw Error("path");
   const body=await readFile(output+name);
   const type=name.endsWith(".js")?"text/javascript":name.endsWith(".css")?"text/css":name.endsWith(".html")?"text/html":name.endsWith(".wasm")?"application/wasm":"image/png";
   res.writeHead(200,{"Content-Type":type});res.end(body);
  }catch{res.writeHead(404);res.end();}
 });
 await new Promise(resolve=>server.listen(0,"127.0.0.1",resolve));
 const origin=`http://127.0.0.1:${server.address().port}`;
 browser=await chromium.launch({executablePath:process.env.CHROME_PATH,headless:true});
 const context=await browser.newContext();
 const page=await context.newPage();page.on("pageerror",e=>issues.push(e.message));
 await page.clock.setFixedTime(new Date("2026-10-16T12:28:00Z"));
 await page.goto(origin);
 const form=page.getByRole("form",{name:"Search Events"});
 await form.getByRole('button',{name:'Cache: All Caches',exact:true}).click();
 for(const name of ['demo','payment','transactions.v1','write'])await form.getByRole('option',{name,exact:true}).click();
 await form.getByLabel('Time Window',{exact:true}).selectOption('15');
 await form.getByLabel("Search state",{exact:true}).selectOption("authorized");
 await form.getByRole("button",{name:"Search",exact:true}).click();
 await page.waitForFunction(()=>document.querySelector("[data-collected-count]")?.textContent?.includes("1 collected"));
 assert.match(await form.locator("[data-matched-count]").textContent(),/^0 matched/);
 assert.equal(requests.length,1);
 assert.equal(requests[0].url.searchParams.get("tap"),binding.tap);
 assert.equal(requests[0].url.searchParams.has("where"),false);
 await form.getByLabel("Search state",{exact:true}).selectOption("pending");
 await form.getByRole("button",{name:"Search",exact:true}).click();
 await page.waitForFunction(()=>document.querySelector("[data-matched-count]")?.textContent?.includes("1 matched"));
 assert.equal(requests.length,1,"field-only search must remain local");
 await form.getByRole("button",{name:"Clear",exact:true}).click();
 assert.equal(requests.length,1);
 assert.equal(await form.getByLabel("Search state",{exact:true}).inputValue(),"");
 await form.getByRole("button",{name:"Refresh",exact:true}).click();
 await page.waitForFunction(()=>document.querySelector("[data-collected-count]")?.textContent?.includes("1 collected"));
 assert.equal(requests.length,2);
 const have=JSON.parse(Buffer.from(requests[1].headers["trunk-coverage"],"base64url"));
 assert.deepEqual(have[0].Index,[{Start:0,End:6}]);
 await page.reload();
 await page.waitForFunction(()=>document.querySelector("[data-collected-count]")?.textContent?.includes("1 collected"));
 assert.equal(requests.length,2,"reload restores stored native bytes without collection");
 assert.match(await form.locator("[data-matched-count]").textContent(),/^1 matched/);
 await page.setViewportSize({width:1200,height:900});
 await page.screenshot({path:"/private/tmp/snoot-search-desktop.png",fullPage:true});
 await page.setViewportSize({width:390,height:844});
 await page.screenshot({path:"/private/tmp/snoot-search-mobile.png"});
 const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth);
 console.log(await page.evaluate(()=>({innerWidth,scroll:document.documentElement.scrollWidth,body:document.body.scrollWidth})));
 if(overflow) console.log(await page.evaluate(()=>[...document.querySelectorAll("body *")]
  .filter(e=>!e.closest(".table-scroll"))
  .map(e=>({tag:e.tagName,cls:e.className,left:e.getBoundingClientRect().left,right:e.getBoundingClientRect().right,width:e.getBoundingClientRect().width}))
  .filter(r=>r.right>innerWidth+1&&r.width>0).slice(0,50)));
 assert.equal(overflow,false,"mobile layout should not overflow horizontally");
 assert.deepEqual(issues,[]);
 console.log("PASS: hierarchy selection → native collection; local object criteria; clear; coverage-only refresh; offline restore.");
 console.log(`Wire fixture: cold ${wire.length} B, warm ${warm.length} B; 2 GETs for collection + refresh, 0 GETs for local search/clear/reload.`);
 await page.setViewportSize({width:1200,height:900});
 await page.getByRole("button",{name:"Collections",exact:true}).click();
 const historyForm=page.getByRole("form",{name:"Object History",exact:true});
 await historyForm.getByLabel("History Resource",{exact:true}).selectOption(binding.tap);
 await historyForm.getByLabel("Object ID",{exact:true}).fill("42");
 await historyForm.getByRole("button",{name:"Load History",exact:true}).click();
 const historyResults=page.getByRole("region",{name:"Object History results"});
 await historyResults.getByText("Complete",{exact:true}).waitFor();
 assert.equal(await historyResults.locator("article").count(),2);
 assert.equal(historyRequests.length,1);assert.equal(historyRequests[0].searchParams.get("id"),"42");
 assert.equal(historyRequests[0].searchParams.get("collection"),"payment/transactions.v1");
 assert.equal(requests.length,2,"object history must not collect a time window");
 await historyResults.locator("article button").first().click();
 assert.match(await historyResults.locator("pre").textContent(),/request completed/);
 await historyResults.getByRole("button",{name:/Save event .* to notebook/}).first().click();
 const notebook=page.getByRole("dialog",{name:"Save to Notebook"});
 await notebook.getByRole("button",{name:"New Notebook",exact:true}).click();
 await notebook.getByLabel("Notebook name",{exact:true}).fill("History evidence");
 await notebook.getByRole("button",{name:"Create & save",exact:true}).click();
 await historyResults.getByText("Saved to History evidence",{exact:true}).waitFor();
 assert.ok(await historyResults.getByRole("button",{name:"Add Indicator for amount",exact:true}).count()>0);
 await historyResults.getByRole("button",{name:"Check Invalidations"}).click();
 await historyResults.getByText("No new invalidations since this history was loaded.",{exact:true}).waitFor();
 await historyResults.getByRole("button",{name:"Check Invalidations"}).click();
 await historyResults.getByText("New invalidation received. Refresh the full object from its origin before replay.",{exact:true}).waitFor();
 await page.screenshot({path:"/private/tmp/snoot-history-desktop.png",fullPage:true});
 await historyForm.getByLabel("Object ID",{exact:true}).fill("disabled");
 await historyForm.getByRole("button",{name:"Load History",exact:true}).click();
 await page.getByRole("alert").filter({hasText:"requires collection.idx"}).waitFor();
 assert.equal(requests.length,2,"disabled history must not fall back to a full scan");
 await page.getByRole("button",{name:"Alerts",exact:true}).click();
 await page.getByRole("tab",{name:"Lost Events",exact:true}).click();
 const auditForm=page.getByRole("form",{name:"Lost Events",exact:true});
 await auditForm.getByLabel("Rejection Source",{exact:true}).selectOption(binding.tap);
 await auditForm.getByRole("button",{name:"Load Lost Events",exact:true}).click();
 const auditResults=page.getByRole("region",{name:"Lost Events results"});
 await auditResults.getByText("Complete",{exact:true}).waitFor();
 assert.equal(auditRequests[0].searchParams.get("tap"),binding.tap);
 await auditResults.locator("summary").click();
 assert.match(await auditResults.locator("pre").textContent(),/18446744073709551615/);
 await page.screenshot({path:"/private/tmp/snoot-rejections-desktop.png",fullPage:true});
 await page.setViewportSize({width:390,height:844});
 await page.screenshot({path:"/private/tmp/snoot-rejections-mobile.png",fullPage:true});
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,"audit layout should fit mobile");
 auditTruncated=true;
 await auditForm.getByRole("button",{name:"Load Lost Events",exact:true}).click();
 await page.getByRole("alert").filter({hasText:"Incomplete rejection inspection"}).waitFor();
 assert.equal(await auditResults.getByText("Complete",{exact:true}).count(),0);
 await page.setViewportSize({width:1200,height:900});
 await page.locator('nav button[title="Search"]').click();
 assert.match(await form.locator("[data-collected-count]").textContent(),/^1 collected/);
 assert.equal(requests.length,2,"inspection must preserve the search working set");
 assert.deepEqual(issues,[]);
 console.log("PASS: cross-minor object-history UI, explicit disabled-index error, invalidation checks, exact rejected-event display, scoped audit requests, truncation detection and preserved search working set.");
 await page.getByRole("button",{name:"Connection",exact:true}).click();
 await page.getByLabel("Resource Cache to Rebuild",{exact:true}).selectOption(binding.tap.split("/").slice(0,3).join("/")+"/write");
 page.once("dialog",dialog=>dialog.accept());
 await page.getByRole("button",{name:"Rebuild Local Cache",exact:true}).click();
 await page.getByText("Local replica reset. Search again to collect from the server.",{exact:true}).waitFor();
 assert.equal(requests.length,2,"local rebuild must not issue a collection request");
 await page.getByRole("button",{name:"Done",exact:true}).click();
 assert.match(await form.locator("[data-collected-count]").textContent(),/^0 collected/);
 await form.getByLabel("Search state",{exact:true}).selectOption("pending");
 await form.getByRole("button",{name:"Search",exact:true}).click();
 await page.waitForFunction(()=>document.querySelector("[data-collected-count]")?.textContent?.includes("1 collected"));
 assert.equal(requests.length,3);assert.equal(requests[2].headers["trunk-coverage"],undefined);
 await page.reload();
 await page.waitForFunction(()=>document.querySelector("[data-collected-count]")?.textContent?.includes("1 collected"));
 assert.equal(requests.length,3);
 assert.deepEqual(issues,[]);
 console.log("PASS: confirmed local cache rebuild clears working set, makes no remote request, recollects without old coverage and restores after reload");

}finally{
 await browser?.close();
 await new Promise(resolve=>server?server.close(resolve):resolve());
 await rm(output,{recursive:true,force:true});
}
