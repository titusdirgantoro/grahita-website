import { cleanup, configure } from "@testing-library/react";
import { afterEach, beforeAll, vi } from "vitest";

/**
 * Setup pengujian komponen.
 *
 * Auto-cleanup bawaan Testing Library hanya aktif bila `globals` menyala;
 * karena proyek ini memakai import eksplisit dari vitest, pembersihan DOM dan
 * global tiruan antar-tes dilakukan di sini supaya markup tes sebelumnya tidak
 * bocor ke tes berikutnya.
 */

/**
 * Anggaran tunggu kueri async dinaikkan dari bawaan 1 dtk menjadi 5 dtk.
 *
 * Halaman portal memuat data dalam dua putaran berantai (sesi lewat /auth/me,
 * lalu data halaman lewat endpoint-nya), jadi kueri async baru bisa menemukan
 * isi setelah dua putaran janji plus render React. Pada host yang sibuk —
 * suite penuh menjalankan delapan berkas uji paralel dengan lingkungan jsdom
 * masing-masing — putaran itu bisa melewati 1 dtk, dan tabel tertinggal di
 * SkeletonList (`aria-busy="true"`, "Memuat data…") sehingga tes gagal
 * sekalipun balasannya sudah dijanjikan mock (F1-11). Anggaran ini hanya
 * menentukan berapa lama tes menunggu, bukan apa yang diuji: bila UI memang
 * tidak pernah memuat data, tes tetap gagal.
 */
configure({ asyncUtilTimeout: 5000 });

beforeAll(() => {
  // jsdom tidak mengimplementasikan scrollTo; router memanggilnya saat navigasi.
  window.scrollTo = () => undefined;
});

afterEach(() => {
  cleanup();
  // Global tiruan (fetch) dilepas sebelum pulihkan spy supaya implementasi asli
  // kembali utuh, lalu spy (mis. window.open) dipulihkan agar tidak bocor ke
  // tes berikutnya di berkas yang sama.
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
