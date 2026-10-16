import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

/**
 * Konfigurasi Customer Portal.
 *
 * Host dan port dapat diatur lewat environment supaya SPA "dapat diakses pada
 * host konfigurabel" (kriteria F1-01): CUSTOMER_HOST dipakai di produksi,
 * VITE_HOST/VITE_PORT untuk pengembangan lokal.
 *
 * Selama pengembangan, /api diproksikan ke service API sehingga browser tetap
 * melihat API same-origin seperti di produksi (Caddy).
 */
export default defineConfig({
  plugins: [react()],
  server: {
    host: process.env.VITE_HOST ?? "localhost",
    port: Number(process.env.VITE_PORT ?? 5173),
    strictPort: true,
    proxy: {
      "/api": {
        target: process.env.VITE_API_TARGET ?? "http://localhost:8088",
        changeOrigin: false,
        headers: process.env.VITE_BROWSER_FIXTURE_PROXY_KEY
          ? { "X-Browser-Fixture-Key": process.env.VITE_BROWSER_FIXTURE_PROXY_KEY }
          : undefined,
      },
    },
  },
  preview: {
    host: process.env.VITE_HOST ?? "localhost",
    port: Number(process.env.VITE_PREVIEW_PORT ?? 4173),
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
    // Kueri async memakai anggaran tunggu 5 dtk (src/test/setup.ts) karena
    // halaman memuat data dua putaran berantai; batas per-tes dinaikkan dari
    // bawaan 5 dtk supaya pada host sibuk anggaran itu sempat terpakai dan
    // kegagalan tetap muncul sebagai pesan kueri, bukan "Test timed out".
    testTimeout: 20000,
  },
});
