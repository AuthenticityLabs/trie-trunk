import { execFileSync } from "node:child_process";
import { copyFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
const root = fileURLToPath(new URL("../../", import.meta.url));
execFileSync(fileURLToPath(new URL("../node_modules/.bin/tsc",import.meta.url)), ["-p", "../pill-packer/npm/tsconfig.json"], {cwd:root,stdio:"inherit"});
execFileSync(process.execPath, ["../pill-packer/scripts/build-wasm.mjs"], {cwd: root, stdio: "inherit"});
copyFileSync(
  new URL("../../../pill-packer/npm/codec.wasm", import.meta.url),
  new URL("../assets/pil.wasm", import.meta.url),
);

execFileSync(
  "cargo",
  [
    "build",
    "--release",
    "--locked",
    "--target",
    "wasm32-unknown-unknown",
    "--manifest-path",
    "rust/browser/Cargo.toml",
  ],
  { cwd: root, stdio: "inherit" },
);
copyFileSync(
  new URL(
    "../../rust/browser/target/wasm32-unknown-unknown/release/trunk_browser.wasm",
    import.meta.url,
  ),
  new URL("../assets/trunk-store.wasm", import.meta.url),
);
