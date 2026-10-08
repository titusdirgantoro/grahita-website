import { defineConfig } from "vitest/config";

/**
 * Klien API diuji di lingkungan Node: ia hanya memakai fetch/Response.
 * DOM cookie dibaca lewat `cookieReader` yang bisa diganti pada pengujian.
 */
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
