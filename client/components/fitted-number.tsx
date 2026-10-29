import {createEffect,onMount,onCleanup} from 'solid-js';

// Measure only on value, available-width, or font changes; chart updates do not
// repeatedly measure an unchanged statistic.
export function FittedNumber(props:{text:string}) {
  let box!:HTMLDivElement,text!:HTMLSpanElement,frame=0,disposed=false;
  const fit=()=>{
    if(disposed||!box||!text)return;
    const width=box.clientWidth;if(!width)return;
    text.style.fontSize='';
    const base=parseFloat(getComputedStyle(text).fontSize),natural=text.getBoundingClientRect().width;
    if(natural>width)text.style.fontSize=`${Math.floor(base*width/natural*100)/100}px`;
  };
  const schedule=()=>{cancelAnimationFrame(frame);frame=requestAnimationFrame(fit)};
  createEffect(()=>{props.text;schedule()});
  onMount(()=>{
    let width=-1;
    const observer=new ResizeObserver(entries=>{
      const next=entries[0].contentRect.width;
      if(next!==width){width=next;schedule()}
    });
    observer.observe(box);
    void document.fonts.ready.then(()=>{if(!disposed)schedule()});
    document.fonts.addEventListener('loadingdone',schedule);
    onCleanup(()=>{disposed=true;observer.disconnect();cancelAnimationFrame(frame);document.fonts.removeEventListener('loadingdone',schedule)});
  });
  return <div ref={box} class="fast-number"><span ref={text}>{props.text}</span></div>;
}
