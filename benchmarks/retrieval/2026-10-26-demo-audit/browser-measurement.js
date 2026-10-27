async () => {
 window.__audit={longTasks:[]};
 new PerformanceObserver(list=>{for(const e of list.getEntries())window.__audit.longTasks.push({start:e.startTime,duration:e.duration})}).observe({type:'longtask'});
 window.__measureSearch=async(refresh=false)=>{
  const old=document.querySelector('[data-search-time-kind="Search"], [data-search-time-kind="Restore"]'),start=performance.now();
  return await new Promise(resolve=>{
   let page,paint,done=false;
   const finish=()=>{if(done||!page||!paint||document.querySelector('.paged-results')?.getAttribute('aria-busy')==='true')return;
    done=true;obs.disconnect();
    const chart=document.querySelector('[data-search-time-kind="Charts"]');
    resolve({...page,wallMs:paint,chartsReadyMs:performance.now()-start,chartMs:Number(chart?.dataset.searchTimeMs),chartDetails:chart?.textContent,nodes:document.querySelectorAll('*').length,rows:document.querySelectorAll('[data-event-row]').length,longTasks:window.__audit.longTasks.filter(e=>e.start>=start),errors:[...document.querySelectorAll('[role="alert"]')].map(e=>e.textContent)});
   };
   const obs=new MutationObserver(()=>{
    const result=document.querySelector('[data-search-time-kind="Search"]');
    if(!page&&result&&result!==old&&document.querySelector('[data-event-row]')){
     page={appMs:Number(result.dataset.searchTimeMs),domReadyMs:performance.now()-start,details:result.textContent};
     requestAnimationFrame(()=>requestAnimationFrame(()=>{paint=performance.now()-start;finish()}));
    }finish();
   });
   obs.observe(document.body,{subtree:true,childList:true,attributes:true});
   if(refresh)[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Refresh').click();
   else document.forms[0].requestSubmit();
   setTimeout(()=>{if(!done){done=true;obs.disconnect();resolve({timeout:true,page,paint})}},25000);
  });
 };return await window.__measureSearch();
}
