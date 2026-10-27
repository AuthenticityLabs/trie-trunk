/** Pixel positions keep the same row; ratios handle a changed viewport. */
export type ScrollPosition = {
 top:number; left:number; vertical?:number; horizontal?:number;
 viewportHeight?:number; viewportWidth?:number;
};
type Metrics = {scrollTop:number;scrollLeft:number;scrollHeight:number;scrollWidth:number;clientHeight:number;clientWidth:number};
export function capturePosition(el:Metrics):ScrollPosition {
 const maxTop=Math.max(0,el.scrollHeight-el.clientHeight),maxLeft=Math.max(0,el.scrollWidth-el.clientWidth);
 const top=Math.max(0,Math.min(maxTop,el.scrollTop)),left=Math.max(0,Math.min(maxLeft,el.scrollLeft));
 return {top,left,vertical:maxTop?top/maxTop:0,horizontal:maxLeft?left/maxLeft:0,
  viewportHeight:el.clientHeight,viewportWidth:el.clientWidth};
}
export function resolvePosition(saved:ScrollPosition,el:Metrics) {
 return {top:saved.viewportHeight!==undefined&&saved.viewportHeight!==el.clientHeight&&saved.vertical!==undefined
   ?saved.vertical*Math.max(0,el.scrollHeight-el.clientHeight):saved.top,
  left:saved.viewportWidth!==undefined&&saved.viewportWidth!==el.clientWidth&&saved.horizontal!==undefined
   ?saved.horizontal*Math.max(0,el.scrollWidth-el.clientWidth):saved.left};
}
export function validPosition(value:unknown):value is ScrollPosition {
 if(!value||typeof value!=='object')return false;
 const p=value as ScrollPosition;
 return Number.isFinite(p.top)&&p.top>=0&&Number.isFinite(p.left)&&p.left>=0&&
  [p.vertical,p.horizontal].every(v=>v===undefined||(Number.isFinite(v)&&v>=0&&v<=1))&&
  [p.viewportHeight,p.viewportWidth].every(v=>v===undefined||(Number.isFinite(v)&&v>=0));
}

/** Recover optional geometry without dropping the user's query or time window. */
export function normalizePosition(value:unknown):ScrollPosition|undefined {
 if(!value||typeof value!=='object')return;
 const p=value as ScrollPosition;
 if(!Number.isFinite(p.top)||!Number.isFinite(p.left))return;
 const result:ScrollPosition={top:Math.max(0,p.top),left:Math.max(0,p.left)};
 for(const key of ['vertical','horizontal'] as const){const n=p[key];if(typeof n==='number'&&Number.isFinite(n))result[key]=Math.max(0,Math.min(1,n));}
 for(const key of ['viewportHeight','viewportWidth'] as const){const n=p[key];if(typeof n==='number'&&Number.isFinite(n)&&n>=0)result[key]=n;}
 return result;
}
