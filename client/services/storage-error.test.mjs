import {test} from 'node:test';
import assert from 'node:assert/strict';
import {storageStep,localStorageRoot} from './store/io-error.ts';
import {describeFileIO} from './store/files.ts';

test('startup storage failure identifies OPFS open without claiming out-of-memory or retrying',async()=>{
 const descriptor=Object.getOwnPropertyDescriptor(globalThis,'navigator');let calls=0;
 const cause=new DOMException('The operation failed for an unknown transient reason (e.g. out of memory).','UnknownError');
 Object.defineProperty(globalThis,'navigator',{configurable:true,value:{storage:{getDirectory:async()=>{calls++;throw cause}}}});
 try{await assert.rejects(localStorageRoot(),error=>error.message.startsWith('Open Local Storage — UnknownError:')&&error.cause===cause);assert.equal(calls,1)}
 finally{if(descriptor)Object.defineProperty(globalThis,'navigator',descriptor);else delete globalThis.navigator}
});
test('optional absent files keep their DOMException identity',async()=>{
 const absent=new DOMException('missing','NotFoundError');
 await assert.rejects(storageStep('Read Local Catalog',async()=>{throw absent}),error=>error===absent);
});
test('file diagnostics retain write size and offset, and do not retry partial mutations',()=>{
 const cause=new DOMException('transient','UnknownError');let calls=0;
 const io=describeFileIO({write(){calls++;throw cause}},'events.sods');
 assert.throws(()=>io.write(new Uint8Array(16),{at:2048}),error=>error.message.includes('16 bytes at 2048')&&error.message.includes('events.sods')&&error.cause===cause);assert.equal(calls,1);
});
