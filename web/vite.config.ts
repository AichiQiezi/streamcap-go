import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": "/src" } },
  server: {
    port: 5178,
    proxy: {
      "/api/ws": { target: "ws://127.0.0.1:6008", ws: true },
      "/api": "http://127.0.0.1:6008",
    },
  },
  build: { chunkSizeWarningLimit: 900 },
});
