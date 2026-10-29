import {build} from 'vite';
import {fileURLToPath} from 'node:url';
const root=fileURLToPath(new URL('../',import.meta.url));
await build({root,base:process.env.BENCH_BASE??'/bench-native/',configFile:false,logLevel:'warn',build:{outDir:'../web/bench-native',emptyOutDir:false,rolldownOptions:{input:root+'benchmarks/native.worker.ts',output:{entryFileNames:'worker.js'}}}});
