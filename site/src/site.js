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
