const archivedCharts=document.querySelector('#archived-homepage-charts [data-home-dashboard]');
if(archivedCharts)import('./archived-homepage-charts.js').then(({initHomepageCharts})=>initHomepageCharts(archivedCharts));

// Static demonstrations: switching views never connects to a live service.
const heroTabs=[...document.querySelectorAll('[data-hero-group]')];
function selectHeroTab(group,index,focus=false){
 const tabs=heroTabs.filter(tab=>tab.dataset.heroGroup===group);
 if(!tabs[index])return;
 tabs.forEach((tab,i)=>{
  const selected=i===index;
  tab.setAttribute('aria-selected',String(selected));
  tab.tabIndex=selected?0:-1;
  document.getElementById(tab.getAttribute('aria-controls')).hidden=!selected;
 });
 if(focus)tabs[index].focus();
}
function revealBenchPanel(target){
 const panel=target?.closest('[data-bench-panel]');
 const tab=panel&&heroTabs.find(tab=>tab.getAttribute('aria-controls')===panel.id);
 if(tab)selectHeroTab('bench',Number(tab.dataset.heroIndex));
}
heroTabs.forEach(tab=>{
 const group=tab.dataset.heroGroup,index=Number(tab.dataset.heroIndex);
 tab.addEventListener('click',()=>selectHeroTab(group,index));
 tab.addEventListener('keydown',event=>{
  const count=heroTabs.filter(item=>item.dataset.heroGroup===group).length;
  let next;
  if(event.key==='ArrowRight'||event.key==='ArrowDown')next=(index+1)%count;
  if(event.key==='ArrowLeft'||event.key==='ArrowUp')next=(index+count-1)%count;
  if(event.key==='Home')next=0;
  if(event.key==='End')next=count-1;
  if(next!==undefined){event.preventDefault();selectHeroTab(group,next,true)}
 });
});

const snootFrames=[...document.querySelectorAll('[data-snoot-preview]')];
if(snootFrames.length){
 const syncPreview=()=>snootFrames.forEach(frame=>frame.contentWindow?.postMessage({type:'snoot-preview-appearance',theme:document.documentElement.dataset.theme,compact:innerWidth<=760},location.origin));
 snootFrames.forEach(frame=>frame.addEventListener('load',syncPreview));
 window.addEventListener('resize',syncPreview);
 new MutationObserver(syncPreview).observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
 syncPreview();
}

const pageSections=[...document.querySelectorAll('[data-page-section]')];
const pageLinks=[...document.querySelectorAll('[data-page-link]')];
if(pageSections.length){
 const header=document.querySelector('.site-header');
 const syncHeaderHeight=()=>document.documentElement.style.setProperty('--hero-header',`${header.getBoundingClientRect().height}px`);
 syncHeaderHeight();
 new ResizeObserver(syncHeaderHeight).observe(header);
 let framePending=false;
 const updateActivePage=()=>{
  framePending=false;
  const top=header.getBoundingClientRect().bottom;
  const visible=page=>{
   const rect=page.getBoundingClientRect();
   return Math.max(0,Math.min(innerHeight,rect.bottom)-Math.max(top,rect.top));
  };
  const active=pageSections.reduce((best,page)=>visible(page)>visible(best)?page:best,pageSections[0]);
  if(visible(active)===0)return;
  pageLinks.forEach(link=>{
   if(link.dataset.pageLink===active.dataset.pageSection)link.setAttribute('aria-current','page');
   else link.removeAttribute('aria-current');
  });
  const hash=active.id==='trunk'?'':`#${active.id}`;
  if(location.hash!==hash)history.replaceState(null,'',`${location.pathname}${location.search}${hash}`);
 };
 const scheduleActivePage=()=>{
  if(!framePending){framePending=true;requestAnimationFrame(updateActivePage)}
 };
 // A tab is a direct destination. Smooth fragment navigation can stop at an
 // intermediate mandatory snap point in WebKit, particularly after a resize.
 const directLinks=[...pageLinks,...document.querySelectorAll('a[href="#bench-storage"]')];
 directLinks.forEach(link=>{
  const target=document.getElementById(link.hash.slice(1));
  if(!target)return;
  link.addEventListener('click',event=>{
   if(event.defaultPrevented||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
   event.preventDefault();
   revealBenchPanel(target);
   target.scrollIntoView({block:'start',behavior:'instant'});
   updateActivePage();
  });
 });
 // Apply an incoming section link before tracking scroll position; the initial
 // Trunk view can erase the hash before the browser scrolls to it.
 const initialHash=location.hash;
 const initialize=async()=>{
  await document.fonts.ready;
  await new Promise(requestAnimationFrame);
  const target=document.getElementById(initialHash.slice(1));
  if(target){revealBenchPanel(target);target.scrollIntoView({block:'start',behavior:'instant'})}
  window.addEventListener('scroll',scheduleActivePage,{passive:true});
  window.addEventListener('resize',scheduleActivePage);
  requestAnimationFrame(updateActivePage);
 };
 if(document.readyState==='complete')initialize();
 else window.addEventListener('load',initialize,{once:true});
}
