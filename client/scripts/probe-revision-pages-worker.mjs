import {operate, verify} from './probe-revision-pages-core.mjs';
self.onmessage = async ({data: {c, action, stage}}) => {
  let handle;
  try {
    const root = await navigator.storage.getDirectory();
    if (action === 'delete') { await root.removeEntry('revision-page-probe', {recursive: true}); self.postMessage({ok: true}); return; }
    const dir = await root.getDirectoryHandle('revision-page-probe', {create: true});
    const entry = await dir.getFileHandle('probe.idx', {create: true});
    handle = await entry.createSyncAccessHandle();
    const file = {
      write: (a, at) => handle.write(a, {at}), read: (a, at) => handle.read(a, {at}),
      truncate: n => handle.truncate(n), flush: () => handle.flush(), size: () => handle.getSize(),
    };
    const result = action === 'verify' ? verify(file, c, stage) : operate(file, c, action);
    handle.close(); handle = undefined;
    self.postMessage({result, usage: await navigator.storage.estimate()});
  } catch (e) { self.postMessage({error: String(e.stack ?? e)}); }
  finally { handle?.close(); }
};
