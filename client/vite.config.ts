import { defineConfig, loadEnv } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig(({ mode }) => {
  const api = loadEnv(mode, ".", "VITE_").VITE_API_PROXY;
  return {
    plugins: [solid()],
    // Optional local API for development; production requests stay same-origin.
    server: api ? {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: Object.fromEntries(
        ["/collect", "/events", "/summary-day", "/schemas", "/caches", "/objects", "/rejections"].map(path => [
          path, api,
        ]),
      ),
    } : undefined,
    build: {
      outDir: "../web",
      emptyOutDir: true,
    },
  };
});
