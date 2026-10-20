/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go binary embeds the build from internal/ui/dist/app.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/ui/dist/app",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // Same host for page and API, so the session cookie and the Origin check work in dev.
    proxy: { "/api": "http://127.0.0.1:8090" },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
