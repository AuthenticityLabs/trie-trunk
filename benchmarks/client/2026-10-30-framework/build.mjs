const {build}=await import(process.cwd()+'/client/node_modules/vite/dist/node/index.js');
import {readFile,writeFile,mkdir,symlink,unlink} from 'node:fs/promises';
const old='/tmp/trie-framework-comparison/pre-library',current=process.cwd();
await symlink(current+'/client/node_modules',old+'/client/node_modules').catch(()=>{});
for(const [root,name]of [[old,'historical'],['/tmp/trie-framework-comparison/pre-redesign/trie-trunk','optimized'],[current,'current']]){
 let code=await readFile(current+'/client/benchmarks/query-path.worker.ts','utf8');
 if(name!=='current')code=code.replace("import {readTrunk} from '../services/pil/trunk.ts';",'').replace("const trunk=await readTrunk({token:'',signal,codec,definitions,fetcher});",'const trunk={catalog:{files:[]}};').replace('const cache=new ViewCache(fetcher);cache.setTrunk(trunk);','let cache=new ViewCache(fetcher);').replace('cache.resetEvents();','cache=new ViewCache(fetcher);');
 // Recorded trials retain normal application caches. resetQueryCache is an optional diagnostic.
 await mkdir(root+'/client/benchmarks',{recursive:true});const input=root+'/client/benchmarks/framework.worker.ts';await writeFile(input,code);
 await build({root:root+'/client',configFile:false,resolve:name==='optimized'?{alias:[{find:'pill-packer/codec.wasm',replacement:'/tmp/trie-framework-comparison/pre-redesign/pill-packer/npm/codec.wasm'},{find:/^pill-packer\/(.+)$/,replacement:'/tmp/trie-framework-comparison/pre-redesign/pill-packer/npm/portable/$1.js'},{find:'pill-packer',replacement:'/tmp/trie-framework-comparison/pre-redesign/pill-packer/npm/index.js'}]}:{},base:'/bench/',logLevel:'warn',build:{outDir:`/tmp/trie-framework-comparison/${name}/web`,emptyOutDir:true,rolldownOptions:{input,output:{entryFileNames:'worker.js'}}}});
 await unlink(input);
}
