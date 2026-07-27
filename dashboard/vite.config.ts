/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Dev server proxies the API and WebSocket to the Go backend on :8080 so the SPA
// runs same-origin in production and dev alike (docs/v0.1/09-frontend.md).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: true,
    // Playwright specs live in e2e/ and must not be collected by Vitest.
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
  },
});
