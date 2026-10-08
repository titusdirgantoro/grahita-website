import { describe, expect, it } from "vitest";

// Dibaca sebagai teks oleh Vite (?raw) supaya tes tetap bebas tipe Node.
import html from "../index.html?raw";

/**
 * Produksi tidak boleh memuat sumber daya dari CDN pihak ketiga
 * (Architecture.md §3.2): font Plus Jakarta Sans dan ikon Phosphor di-self-host
 * lewat bundel. Uji ini menjaga index.html tetap bebas URL eksternal; keluaran
 * build (dist) diperiksa terpisah saat verifikasi.
 */
describe("index.html tanpa CDN", () => {
  it("tidak memuat URL http(s) apa pun", () => {
    expect(html).not.toMatch(/https?:\/\//i);
  });

  it("memuat bundel lokal sebagai modul", () => {
    expect(html).toContain('src="/src/main.tsx"');
  });
});
