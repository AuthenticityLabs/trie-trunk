import { build } from 'vite';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../', import.meta.url));
await build({
  root,
  base: process.env.BENCH_BASE ?? '/bench-planner/',
  configFile: false,
  logLevel: 'warn',
  build: {
    outDir: process.env.BENCH_OUT ?? '../web/bench-planner',
    emptyOutDir: false,
    rolldownOptions: {
      input: root + 'benchmarks/planner.worker.ts',
      output: {
        entryFileNames: 'worker.js'
      }
    }
  }
});
