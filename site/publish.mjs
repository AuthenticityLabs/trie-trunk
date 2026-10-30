import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {readFileSync,writeFileSync} from 'node:fs';
import {dirname,join} from 'node:path';
import {fileURLToPath} from 'node:url';

const here=dirname(fileURLToPath(import.meta.url));
const root=dirname(here);
const git=(...args)=>execFileSync('git',args,{cwd:root,encoding:'utf8'}).trim();
if(git('status','--porcelain','--untracked-files=all','--','site')){
 throw new Error('Commit the website sources before publishing the combined release.');
}
const revision=git('rev-parse','HEAD');
// Always rebuild every route together from the same committed sources.
const env={...process.env,BENCH_PREVIEW:'0'};
execFileSync(process.execPath,[join(here,'build.mjs')],{cwd:root,env,stdio:'inherit'});
if(git('rev-parse','HEAD')!==revision||git('status','--porcelain','--untracked-files=all','--','site')){
 throw new Error('Website sources changed during the build. Rebuild from committed sources before publishing.');
}
const pages=Object.fromEntries([['/','index.html'],['/PIL/','PIL/index.html'],['/hidden/','hidden/index.html']].map(([route,file])=>[
 route,createHash('sha256').update(readFileSync(join(here,'dist',file))).digest('hex')
]));
writeFileSync(join(here,'dist/release.json'),JSON.stringify({revision,pages},null,2)+'\n');
execFileSync('wrangler',['pages','deploy','dist','--project-name','trietrunk','--branch','main','--commit-hash',revision],{cwd:here,env,stdio:'inherit'});
