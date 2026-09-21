import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": new URL("./src", import.meta.url).pathname } },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:18080", changeOrigin: false },
      "/healthz": "http://127.0.0.1:18080",
      "/openapi.json": "http://127.0.0.1:18080",
    },
  },
  build: { sourcemap: false },
});
