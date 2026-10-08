import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

/**
 * Konfigurasi pengujian paket UI.
 *
 * Komponen diuji di jsdom dengan Testing Library. CSS Modules diproses Vite
 * sehingga kelas yang dihasilkan tetap bisa dipakai dalam assertion (mis.
 * pencocokan kelas `.btn`).
 */
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
