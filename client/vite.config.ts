import { defineConfig, loadEnv, searchForWorkspaceRoot } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig(({ mode }) => {
  const api = loadEnv(mode, ".", "VITE_").VITE_API_PROXY;
  return {
    plugins: [solid()],
    // Optional local API for development; production requests stay same-origin.
    server: {
      fs: {allow: [searchForWorkspaceRoot("."), "../../pill-packer/npm/codec.wasm"]},
      ...(api ? {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: Object.fromEntries(
        ["/query-delta", "/trunk", "/collect", "/events", "/summaries", "/schemas", "/caches", "/objects", "/collections", "/rejections",
          '^/(?:[^/]+/)+(?:resource\\.rx|(?:resource|state|read|write|error|debug|trace)\\.(?:tri|sum|idx|tvy|pods))(?:\\?|$)',
        ].map(path => [
          path, api,
        ]),
      ),
    } : {}),
    },
    build: {
      outDir: "../web",
      emptyOutDir: true,
    },
  };
});
