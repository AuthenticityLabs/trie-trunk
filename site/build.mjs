import {mkdir,readFile,writeFile,cp,rm} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {dirname,join} from 'node:path';
import {createHash} from 'node:crypto';
import {render,stages,pages} from './src/pages.mjs';
import {pagePath,homeRedirects} from './src/routes.mjs';
import {benchmark,measurementReport} from './src/measurements.mjs';
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
const ringsName=await asset('quota-rings.js',await readFile(join(here,'../shared/quota-rings.mjs')));
const js=await asset('site.js',(await readFile(join(here,'src/site.js'),'utf8')).replace('./lifecycle-data.js',`./${dataName}`).replace('./quota-rings.mjs',`./${ringsName}`));
for(const name of Object.keys(pages)){
 if(name==='trunk')continue;
 await mkdir(join(out,name),{recursive:true});
 await writeFile(join(out,name,'index.html'),render(name,css,js));
}
await writeFile(join(out,'index.html'),render('trunk',css,js));
await writeFile(join(out,'_redirects'),homeRedirects.map(path=>`${path} / 301`).join('\n')+'\n');
await cp(join(here,'assets'),join(out,'assets'),{recursive:true});
for(const name of ['snoot-forward.png','snoot-wordmark.png'])
 await cp(join(here,'../client/assets',name),join(out,'assets/brand',name));
await cp(join(here,'../../pill-packer/schemas/definition.cddl'),join(out,'definition.cddl'));
const pillReadme=await readFile(join(here,'../../pill-packer/README.md'),'utf8');
const formatReference=pillReadme.split('## Format Reference\n')[1]?.split('\n## License\n')[0];
if(!formatReference)throw new Error('Pill Packer README is missing its Format Reference');
await writeFile(join(out,'pill-packer.txt'),'# PIL Format Reference\n'+formatReference.replaceAll('](schemas/definition.cddl)','](/definition.cddl)'));
await writeFile(join(out,'trunk-definition.cddl'), 'entry = trunk-definition\n' + await readFile(join(here,'../../pill-packer/schemas/definition.cddl'),'utf8') + await readFile(join(here,'../schemas/trunk-definition.cddl'),'utf8'));
await writeFile(join(out,'measurements.txt'),measurementReport);
await mkdir(join(out,'measurements'),{recursive:true});
for(const name of ['footprint.json','compression.json','compression-timing.json','summary-comparison.json','metadata.json'])
 await cp(join(here,'../benchmarks/storage',benchmark,name),join(out,'measurements',name));
await writeFile(join(out,'robots.txt'),'User-agent: *\nAllow: /\nSitemap: https://trietrunk.com/sitemap.xml\n');
await writeFile(join(out,'sitemap.xml'),`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${Object.keys(pages).map(p=>`<url><loc>https://trietrunk.com${pagePath(p)}</loc></url>`).join('')}</urlset>`);
console.log(`Built site/dist — ${Object.keys(pages).map(pagePath).join(', ')}`);
