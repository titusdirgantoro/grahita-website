/**
 * @grahita/ui — paket UI bersama kedua portal.
 *
 * Kartu F1-03 mengisi paket ini dengan komponen hasil port prototype
 * (tombol, form, tabel, chip, modal, state kosong/skeleton). Pada F1-01 paket
 * ini baru menyediakan token desain dan helper kelas nama.
 */

import "./tokens.css";

/**
 * cn menggabungkan kelas nama secara kondisional tanpa dependensi tambahan.
 */
export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

/** Nama token yang dijamin tersedia di kedua portal. */
export const NAMA_TOKEN = {
  primary: "--brand-primary",
  secondary: "--brand-secondary",
  accent: "--brand-accent",
  ink: "--ink",
  surface: "--surface",
  line: "--line",
  text: "--text",
  textMuted: "--text-muted",
} as const;
