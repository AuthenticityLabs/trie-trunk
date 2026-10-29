import {clearStorage,type StorageTarget} from '../local-storage';
import {useSyncFileWrites} from './atomic-file';
useSyncFileWrites();
self.onmessage=async({data}:{data:StorageTarget})=>{
 try{await clearStorage(data);self.postMessage({ok:true})}
 catch(error){self.postMessage({error:error instanceof Error?error.message:String(error)})}
};
