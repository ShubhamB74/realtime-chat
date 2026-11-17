import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In development, /api/* is proxied to the Go backend so the browser sees one origin.
const apiTarget = process.env.VITE_API_PROXY ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: apiTarget,
        changeOrigin: true,
        ws: true, // proxy WebSocket upgrades (/api/v1/ws) too
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
    },
  },
});
