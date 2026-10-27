// The same 28 stationary ring paths are used in every view.
export function ringPath(radius, index = 0) {
  return Array.from({length:97},(_,i)=>{
    const angle=i/96*Math.PI*2;
    const r=radius*(1+.025*Math.sin(angle*3)+.018*Math.cos(angle*5+index*.09));
    return `${i?'L':'M'}${(160+Math.cos(angle)*r).toFixed(2)},${(160+Math.sin(angle)*r*.94).toFixed(2)}`;
  }).join(' ')+' Z';
}
export function quotaRings(quota, used, maximum) {
  const fraction=Math.min(1,Math.max(0,quota)/Math.max(1,maximum));
  const boundary=Math.round(fraction*28);
  const free=boundary*(1-Math.min(1,Math.max(0,used)/Math.max(1,quota)));
  return Array.from({length:28},(_,i)=>({
    role:i>=boundary?'bark':i<free?'emptyRing':'filledRing',
    d:ringPath(16+i*4.6,i),
  }));
}
