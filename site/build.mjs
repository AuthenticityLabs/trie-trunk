import {mkdir,readFile,writeFile,cp,rm} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {dirname,join} from 'node:path';
import {createHash} from 'node:crypto';
import {render,stages,pages} from './src/pages.mjs';
import {measurementReport} from './src/measurements.mjs';
const here=dirname(fileURLToPath(import.meta.url)), out=join(here,'dist');
await rm(out,{recursive:true,force:true});
await mkdir(join(out,'assets'),{recursive:true});
const asset=async(name,raw)=>{
 const hash=createHash('sha256').update(raw).digest('hex').slice(0,8);
 const filename=name.replace('.',`-${hash}.`);
 await writeFile(join(out,'assets',filename),raw);return filename;
};
const css=await asset('site.css',await readFile(join(here,'src/site.css')));
// Keep the browser module tiny: do not ship page HTML or build-time templates.
const data=`export const stages=${JSON.stringify(stages)};`;
const dataName=await asset('lifecycle.js',data);
const js=await asset('site.js',(await readFile(join(here,'src/site.js'),'utf8')).replace('./lifecycle-data.js',`./${dataName}`));
for(const name of Object.keys(pages)){
 await mkdir(join(out,name),{recursive:true});
 await writeFile(join(out,name,'index.html'),render(name,css,js));
}
await writeFile(join(out,'index.html'),render('trunk',css,js));
await cp(join(here,'assets'),join(out,'assets'),{recursive:true});
await cp(join(here,'../client/assets/snoot-forward.png'),join(out,'assets/brand/snoot-forward.png'));
await cp(join(here,'../pil/definition.cddl'),join(out,'definition.cddl'));
await writeFile(join(out,'measurements.txt'),measurementReport);
await writeFile(join(out,'robots.txt'),'User-agent: *\nAllow: /\nSitemap: https://trietrunk.com/sitemap.xml\n');
await writeFile(join(out,'sitemap.xml'),`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${Object.keys(pages).map(p=>`<url><loc>https://trietrunk.com/${p}</loc></url>`).join('')}</urlset>`);
console.log('Built site/dist — /trunk, /snoot, /pil, /logger');
