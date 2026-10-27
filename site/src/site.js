import {quotaRings} from './quota-rings.mjs';
import {stages} from './lifecycle-data.js';

// Show one brand at a time: the main lockup hands off to the sticky masthead.
const mainBrand = document.querySelector('.main-brand');
if(mainBrand && 'IntersectionObserver' in window) {
 let brandObserver;
 const observeBrand = () => {
  brandObserver?.disconnect();
  const headerHeight = document.querySelector('.site-header').getBoundingClientRect().height;
  brandObserver = new IntersectionObserver(entries=>{
   document.body.classList.toggle('main-brand-out',!entries[0].isIntersecting);
  },{rootMargin:`-${headerHeight}px 0px 0px 0px`});
  brandObserver.observe(mainBrand);
 };
 observeBrand();
 window.addEventListener('resize',observeBrand);
}

const tabs = [...document.querySelectorAll('[data-stage]')];
function select(index, focus = false) {
 const stage = stages[index];
 if (!stage) return;
 tabs.forEach((tab,i) => {
  tab.setAttribute('aria-selected',String(index===i));
  tab.tabIndex=index===i?0:-1;
 });
 document.querySelector('#stage-panel').setAttribute('aria-labelledby',`stage-${index}`);
 document.querySelector('#stage-place').textContent=stage.location;
 document.querySelector('#stage-title').textContent=stage.action;
 const acronym = document.querySelector('#stage-acronym');
 acronym.textContent=stage.acronym;
 acronym.hidden=!stage.acronym;
 const note = document.querySelector('#stage-note');
 // The linked notation is static, authored lifecycle copy.
 note.innerHTML=stage.note??'';
 note.hidden=!stage.note;
 document.querySelector('#stage-copy').textContent=stage.copy;
 document.querySelector('#stage-foot').textContent=stage.foot;
 // Scene markup is static, authored build data; no remote or user input enters it.
 document.querySelector('#stage-scene').innerHTML=stage.scene;
 if (focus) tabs[index].focus();
}
tabs.forEach((tab,i) => {
 tab.addEventListener('click',()=>select(i));
 tab.addEventListener('keydown',event=>{
  let next;
  if(event.key==='ArrowRight')next=(i+1)%tabs.length;
  if(event.key==='ArrowLeft')next=(i+tabs.length-1)%tabs.length;
  if(event.key==='Home')next=0;
  if(event.key==='End')next=tabs.length-1;
  if(next!==undefined){event.preventDefault();select(next,true)}
 });
});

const syntaxTabs = [...document.querySelectorAll('[data-syntax]')];
function selectSyntax(index, focus = false) {
 syntaxTabs.forEach((tab,i)=>{
  const selected=i===index;
  tab.setAttribute('aria-selected',String(selected));
  tab.tabIndex=selected?0:-1;
  document.getElementById(tab.getAttribute('aria-controls')).hidden=!selected;
 });
 if(focus)syntaxTabs[index].focus();
}
syntaxTabs.forEach((tab,i)=>{
 tab.addEventListener('click',()=>selectSyntax(i));
 tab.addEventListener('keydown',event=>{
  let next;
  if(event.key==='ArrowRight')next=(i+1)%syntaxTabs.length;
  if(event.key==='ArrowLeft')next=(i+syntaxTabs.length-1)%syntaxTabs.length;
  if(event.key==='Home')next=0;
  if(event.key==='End')next=syntaxTabs.length-1;
  if(next!==undefined){event.preventDefault();selectSyntax(next,true)}
 });
});

const themeButtons = [...document.querySelectorAll('[data-theme-choice]')];
const systemTheme = matchMedia('(prefers-color-scheme: dark)');
let themePreference = document.documentElement.dataset.themePreference ?? 'auto';
function applyTheme(preference) {
 themePreference = ['light','dark'].includes(preference) ? preference : 'auto';
 const theme = themePreference==='auto' ? (systemTheme.matches?'dark':'light') : themePreference;
 document.documentElement.dataset.theme = theme;
 document.documentElement.dataset.themePreference = themePreference;
 document.querySelector('meta[name="theme-color"]').content=theme==='dark'?'#181818':'#fafafa';
 themeButtons.forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.themeChoice===themePreference)));
}
themeButtons.forEach(button=>button.addEventListener('click',()=>{
 applyTheme(button.dataset.themeChoice);
 try { localStorage.setItem('trietrunk.theme',themePreference); } catch {}
}));
systemTheme.addEventListener('change',()=>{if(themePreference==='auto')applyTheme('auto')});
window.addEventListener('storage',event=>{
 if(event.key==='trietrunk.theme'||event.key===null)applyTheme(event.newValue);
});
applyTheme(themePreference);

// Keep the three trie explanations in one view, including direct section links.
const trieTabs = [...document.querySelectorAll('[data-trie-tab]')];
function selectTrie(index, focus = false, updateHash = false) {
 if (!trieTabs[index]) return;
 trieTabs.forEach((tab,i)=>{
  const selected=i===index;
  tab.setAttribute('aria-selected',String(selected));
  tab.tabIndex=selected?0:-1;
  document.getElementById(tab.dataset.trieTab).hidden=!selected;
 });
 if (focus) trieTabs[index].focus();
 if (updateHash) history.replaceState(null,'',index===0?'#tries':`#${trieTabs[index].dataset.trieTab}`);
}
trieTabs.forEach((tab,index)=>{
 tab.addEventListener('click',()=>selectTrie(index,false,true));
 tab.addEventListener('keydown',event=>{
  let next;
  if(event.key==='ArrowRight')next=(index+1)%trieTabs.length;
  if(event.key==='ArrowLeft')next=(index+trieTabs.length-1)%trieTabs.length;
  if(event.key==='Home')next=0;
  if(event.key==='End')next=trieTabs.length-1;
  if(next!==undefined){event.preventDefault();selectTrie(next,true,true)}
 });
});
function selectTrieFromHash() {
 const id=location.hash.slice(1);
 const index=id==='tries'?0:trieTabs.findIndex(tab=>tab.dataset.trieTab===id);
 if(index<0||!trieTabs.length)return;
 selectTrie(index);
 document.getElementById('tries').scrollIntoView({block:'start',behavior:'instant'});
}
window.addEventListener('hashchange',selectTrieFromHash);
selectTrieFromHash();

// Highlight the same taxonomy tier in the diagram and its explanatory controls.
const taxonomyButtons = [...document.querySelectorAll('[data-taxonomy-tier]')];
taxonomyButtons.forEach(button=>button.addEventListener('click',()=>{
 const tier=button.dataset.taxonomyTier;
 document.querySelector('.taxonomy-mobile-copy').textContent=button.querySelector('span>span').textContent;
 taxonomyButtons.forEach(item=>item.setAttribute('aria-pressed',String(item===button)));
 document.querySelectorAll('[data-tree-tier]').forEach(node=>{
  node.classList.toggle('is-active',node.dataset.treeTier===tier);
 });
}));

const aggregatePeriods = ['Yearly','Monthly','Daily','Hourly','15-Minute','5-Minute'];
const aggregateCounts = [8,12,30,24,96,288];
const aggregateLabels = [['Year 1','Year 8'],['Jan','Dec'],['Day 1','Day 30'],['00:00','23:00'],['00:00','23:45'],['00:00','23:55']];
document.querySelectorAll('[data-aggregate-period]').forEach(button=>button.addEventListener('click',()=>{
 const index=Number(button.dataset.aggregatePeriod), count=aggregateCounts[index];
 const group=document.getElementById('aggregate-bars');
 group.replaceChildren();
 for(let i=0;i<count;i++) {
  const rect=document.createElementNS('http://www.w3.org/2000/svg','rect');
  const height=25+(Math.sin(i/count*12)*.5+.5)*40+((i*31+17)%30);
  const width=420/count;
  Object.entries({x:i*width+width*.16,y:125-height,width:width*.68,height,rx:Math.min(2,width*.1)}).forEach(([key,value])=>rect.setAttribute(key,String(value)));
  group.append(rect);
 }
 document.getElementById('aggregate-title').textContent=`${aggregatePeriods[index]} View`;
 document.getElementById('aggregate-from').textContent=aggregateLabels[index][0];
 document.getElementById('aggregate-to').textContent=aggregateLabels[index][1];
 document.querySelectorAll('[data-aggregate-period]').forEach(item=>item.setAttribute('aria-pressed',String(item===button)));
}));
const retentionBudget=document.getElementById('retention-budget');
// This public illustration uses an explicit example rate, not a live measurement.
const exampleGBPerDay=4;
const updateRetention=(gigabytes)=>{
 const value=Math.min(Number(retentionBudget.max),Math.max(Number(retentionBudget.min),gigabytes));
 const days=value/exampleGBPerDay;
 const format=n=>n.toLocaleString(undefined,{maximumFractionDigits:1});
 const wholeDays=days.toLocaleString(undefined,{maximumFractionDigits:0});
 retentionBudget.value=String(value);
 document.getElementById('retention-value').textContent=`${format(value)} GB`;
 document.getElementById('retention-days').textContent=`${wholeDays} Days`;
 retentionBudget.setAttribute('aria-valuetext',`${format(value)} GB, approximately ${wholeDays} Days`);
 const group=document.getElementById('quota-rings');
 group.replaceChildren(...quotaRings(value,0,256).map(p=>{
  const path=document.createElementNS('http://www.w3.org/2000/svg','path');
  path.setAttribute('class',`quota-${p.role}`);path.setAttribute('d',p.d);return path;
 }));
};
retentionBudget?.addEventListener('input',()=>updateRetention(Number(retentionBudget.value)));
