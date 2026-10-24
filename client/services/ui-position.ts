/** Pixel positions keep the same row; ratios handle a changed viewport. */
export type ScrollPosition = {
 top:number; left:number; vertical?:number; horizontal?:number;
 viewportHeight?:number; viewportWidth?:number;
};
type Metrics = {scrollTop:number;scrollLeft:number;scrollHeight:number;scrollWidth:number;clientHeight:number;clientWidth:number};
export function capturePosition(el:Metrics):ScrollPosition {
 return {top:el.scrollTop,left:el.scrollLeft,
  vertical:el.scrollTop/Math.max(1,el.scrollHeight-el.clientHeight),
  horizontal:el.scrollLeft/Math.max(1,el.scrollWidth-el.clientWidth),
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
