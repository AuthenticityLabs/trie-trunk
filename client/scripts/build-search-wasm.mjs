import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const root=fileURLToPath(new URL('../../',import.meta.url));
const bindgen=process.env.WASM_BINDGEN??'wasm-bindgen';
if(execFileSync(bindgen,['--version'],{encoding:'utf8'}).trim()!=='wasm-bindgen 0.2.129')throw Error('Install wasm-bindgen-cli 0.2.129 to match the locked reader');
execFileSync('cargo',['build','--release','--locked','--target','wasm32-unknown-unknown','--manifest-path','rust/search-wasm/Cargo.toml'],{cwd:root,stdio:'inherit'});
execFileSync(bindgen,['--target','web','--out-dir','client/assets/search','--out-name','search','rust/search-wasm/target/wasm32-unknown-unknown/release/trunk_search_wasm.wasm'],{cwd:root,stdio:'inherit'});
