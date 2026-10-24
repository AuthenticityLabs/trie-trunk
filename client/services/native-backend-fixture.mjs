// In-memory durable file/metadata model for failure-injection tests.
class File{
 data=new Uint8Array();
 getSize(){return this.data.length;}
 read(b,{at}){const v=this.data.subarray(at,at+b.length);b.set(v);return v.length;}
 write(b,{at}){if(at+b.length>this.data.length)this.truncate(at+b.length);this.data.set(b,at);return b.length;}
 truncate(n){const out=new Uint8Array(n);out.set(this.data.subarray(0,n));this.data=out;}
 flush(){} close(){}
}
export class Backend{
 sources=new Map();files=new Map();fail=false;
 stateControl={serial:0,scopes:{}};
 async control(){return structuredClone(this.stateControl);}
 async get(k){return structuredClone(this.sources.get(k));}
 async list(){return structuredClone([...this.sources.values()]);}
 async put(sources,control){if(this.fail){this.fail=false;throw Error("crash before metadata commit");}for(const s of sources)this.sources.set(s.key,structuredClone(s));if(control)this.stateControl=structuredClone(control);}
 async file(k,kind,revision=0){const key=k+kind+(kind==="state"&&revision?"."+revision:"");if(!this.files.has(key))this.files.set(key,new File());return this.files.get(key);}
 async removeFiles(k){for(const key of this.files.keys())if(key.startsWith(k))this.files.delete(key);}
 async removeOldStateFiles(k,revision){const keep=k+"state"+(revision?"."+revision:"");for(const key of this.files.keys())if(key.startsWith(k+"state")&&key!==keep)this.files.delete(key);}
}
