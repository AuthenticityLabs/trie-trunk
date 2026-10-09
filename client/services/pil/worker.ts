import { storagePort } from "../store/client.ts";
import wasmURL from "../../assets/pil.wasm?url";
import { workerService } from "./worker-runtime.ts";
import { checkResponse } from "./stream.ts";
const modulePromise = fetch(wasmURL).then(async (response) => {
  await checkResponse(response);
  return WebAssembly.compileStreaming(response);
});
modulePromise.catch(() => {});
const service = workerService(modulePromise);
function connect(port: MessagePort) {
  port.onmessage = (event) => {
    if (event.data?.kind === "connect-storage" && event.ports[0])
      service(port, storagePort(event.ports[0]));
  };
  port.start();
}
// SharedWorker on supported browsers. The dedicated-worker fallback uses the
// identical binary protocol and a transferred MessagePort, never main-thread JSONL.
const scope = globalThis as typeof globalThis & {
  onconnect: (event: MessageEvent) => void;
};
if ("onconnect" in globalThis)
  scope.onconnect = (event) => connect(event.ports[0]);
else
  globalThis.onmessage = (event) => {
    if (event.data?.kind === "connect") connect(event.ports[0]);
  };
