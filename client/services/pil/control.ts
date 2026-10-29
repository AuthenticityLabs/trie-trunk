import {decodeObject as decodeValue} from 'pill-packer/object';
export {encodeObject} from 'pill-packer/object';
// Transport metadata exposes named properties to the application. Keep the
// generic PIL codec's Map representation intact outside this explicit view.
export function decodeObject(raw:Uint8Array):unknown {return objectView(decodeValue(raw))}
export function objectView(value:unknown):unknown {
 const project=(v:any):any=>{
  if(v instanceof Map){const out=Object.create(null);for(const [k,x] of v){const key=String(k);if(Object.hasOwn(out,key))throw Error('Ambiguous metadata key');out[key]=project(x)}return out}
  if(Array.isArray(v))return v.map(project);
  if(v&&typeof v==='object'&&!(v instanceof Uint8Array)){const out=Object.create(null);for(const [k,x] of Object.entries(v))out[k]=project(x);return out}
  return v;
 };return project(value);
}
