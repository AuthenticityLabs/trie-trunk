import { execFileSync } from "node:child_process";
import { copyFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
const root = fileURLToPath(new URL("../../", import.meta.url));
execFileSync(
  "cargo",
  [
    "build",
    "--release",
    "--locked",
    "--target",
    "wasm32-unknown-unknown",
    "--manifest-path",
    "pil/wasm/Cargo.toml",
  ],
  { cwd: root, stdio: "inherit" },
);
copyFileSync(
  new URL(
    "../../pil/wasm/target/wasm32-unknown-unknown/release/pil_wasm.wasm",
    import.meta.url,
  ),
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
