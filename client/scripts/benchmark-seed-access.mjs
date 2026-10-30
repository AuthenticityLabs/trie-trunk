const {NativeEvent,NativeTrie}=await import(process.env.SEED_BENCH_MODULE??'../services/seeds/index.ts');
import {encodeObject} from 'pill-packer/object';
import {writeFileSync} from 'node:fs';
const fields=[{name:'original',type:'UUID'},{name:'status',type:'STRING',values:['pending','paid']},{name:'duration',type:'U32'},{name:'@event_id',type:'U24'},{name:'@time',type:'TIMESTAMP_MS'},{name:'@leaf',type:'REF16'}];
const def={pil:0,fields,resource:{roles:{event_id:'original',shared:'@leaf',duration:'duration'}}};
const keys=new Map(fields.map((f,key)=>[key,{key,name:f.name,uuid:f.type==='UUID',enums:new Map((f.values??[]).map((v,i)=>[i,v]))}]));
const mapping={native:true,definition:def,keys:[0,1,2,3,4,5],dictionary:{id:keys.get(0),fields:{keys,names:new Map([...keys.values()].map(f=>[f.name,f]))},path:new Set([1]),tap:'prod/payment/transactions.v1/write'}};
const trie=new NativeTrie(mapping);trie.append(1,encodeObject([{'0':0,'1':1,'2':Uint8Array.of(1)}]));
const hour=Date.UTC(2026,9,29,12),row=new Uint8Array(6);new DataView(row.buffer).setUint32(0,hour%2**32,true);row[4]=1;
const payload=Uint8Array.of(10,0,123,0,0,0,0,0,0),runs=[];
for(let r=0;r<7;r++){let sum=0;const start=performance.now();for(let i=0;i<200000;i++){const e=new NativeEvent(row,payload,trie,undefined,hour);sum+=e.read(2)+e.read(1);}const ms=performance.now()-start;if(sum!==24800000)throw Error(String(sum));if(r>=2)runs.push(ms);}
const report={records:200000,runs,median:[...runs].sort((a,b)=>a-b)[2],runtime:process.version};
if(process.argv[2])writeFileSync(process.argv[2],JSON.stringify(report,null,2)+'\n');console.log(report);
