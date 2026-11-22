/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

// Two pages: the app (index.html) and the Swagger UI (api-docs.html, served at /api/docs).
// `pnpm dev` proxies the API to a local `make run` (https://localhost:8443).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1600, // the Swagger UI page bundles swagger-ui (no CDN)
    rollupOptions: {
      input: { main: resolve(__dirname, "index.html"), docs: resolve(__dirname, "api-docs.html") },
    },
  },
  server: {
    proxy: {
      "/api": { target: "https://localhost:8443", secure: false },
      "/healthz": { target: "https://localhost:8443", secure: false },
    },
  },
  test: { environment: "jsdom", globals: true, setupFiles: ["src/setupTests.ts"] },
});
