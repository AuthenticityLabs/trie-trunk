import {createServer} from 'node:http';
import {readFile,stat} from 'node:fs/promises';
import {resolve,extname,sep,dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {redirects} from './src/routes.mjs';
const root=resolve(dirname(fileURLToPath(import.meta.url)),process.env.BENCH_PREVIEW==='1'?'artifacts/bench-preview':'dist');
const port=Number(process.env.PORT??3030);
const types={'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css; charset=utf-8','.svg':'image/svg+xml','.png':'image/png','.jpg':'image/jpeg','.webp':'image/webp','.woff2':'font/woff2','.ttf':'font/ttf','.cddl':'text/plain; charset=utf-8','.xml':'application/xml','.txt':'text/plain'};
createServer(async(req,res)=>{
 try{
  if(!['GET','HEAD'].includes(req.method)){res.writeHead(405,{Allow:'GET, HEAD'});return res.end()}
  const url=new URL(req.url,'http://localhost');
  const path=decodeURIComponent(url.pathname);
  const redirect=redirects.find(({from})=>from===path);
  if(redirect){const target=new URL(redirect.to,'http://localhost');target.search=url.search;res.writeHead(redirect.status,{Location:`${target.pathname}${target.search}${target.hash}`});return res.end()}
  let file=resolve(root,`.${path}`);
  if(file!==root&&!file.startsWith(root+sep)){res.writeHead(404);return res.end()}
  if((await stat(file)).isDirectory())file=resolve(file,'index.html');
  const bytes=await readFile(file);
  res.writeHead(200,{'Content-Type':types[extname(file)]??'application/octet-stream','Content-Length':bytes.length,'Cache-Control':'no-cache'});
  res.end(req.method==='HEAD'?undefined:bytes);
 }catch{res.writeHead(404,{'Content-Type':'text/plain'});res.end('Not Found')}
}).listen(port,'127.0.0.1',()=>console.log(`Trie-Trunk preview: http://127.0.0.1:${port}`));
