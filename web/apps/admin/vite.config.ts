import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

/**
 * Konfigurasi Admin Portal.
 *
 * Rute admin berada di root host admin (grahita-admin.tiusclaw.com), sesuai
 * keputusan domain user; prefix /admin pada dokumen hanya berlaku untuk
 * endpoint API (/api/v1/admin).
 */
export default defineConfig({
  plugins: [react()],
  server: {
    host: process.env.VITE_HOST ?? "localhost",
    port: Number(process.env.VITE_PORT ?? 5174),
    strictPort: true,
    proxy: {
      "/api": {
        target: process.env.VITE_API_TARGET ?? "http://localhost:8088",
        changeOrigin: false,
      },
    },
  },
  preview: {
    host: process.env.VITE_HOST ?? "localhost",
    port: Number(process.env.VITE_PREVIEW_PORT ?? 4174),
    strictPort: true,
  },
  build: {
    outDir: "dist",
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
