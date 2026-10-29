import {test} from 'node:test';
import assert from 'node:assert/strict';
import {MessageChannel} from 'node:worker_threads';
import {storagePort,storageTransfers} from './store/client.ts';
test('hour PAC ownership transfers without detaching borrowed index or Definition buffers',async()=>{
 const channel=new MessageChannel(),archive=storagePort(channel.port1);
 const bytes=new Uint8Array(4096).fill(7),bounds=Uint32Array.of(0,4096),index=new Uint8Array(6),definition=new Uint8Array(10);
 channel.port2.on('message',({id,command})=>{assert.equal(command.records[0].record.bytes.length,4096);assert.equal(command.records[0].record.bytes[4095],7);channel.port2.postMessage({id,reply:{}})});
 try{
  await archive('',{kind:'native-records',session:'hour',records:[{record:{kind:'pac',bytes,bounds}},{record:{kind:'chunk',file:'index',bytes:index}},{record:{kind:'manifest',manifest:{Definition:definition}}}]});
  assert.equal(bytes.byteLength,0);assert.equal(bounds.byteLength,0);assert.equal(index.byteLength,6);assert.equal(definition.byteLength,10);
 }finally{archive.close();channel.port2.close()}
 const borrowed=new Uint8Array(100),slice=borrowed.subarray(10,20);
 assert.deepEqual(storageTransfers({kind:'native-finish',session:'hour',records:[{record:{kind:'pac',bytes:slice,bounds:new Uint32Array(borrowed.buffer,0,1)}}]}),[]);
});
