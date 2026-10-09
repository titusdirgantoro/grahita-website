import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

/**
 * Uji pTutorial (kartu F1-19): empat topik panduan (memesan, bukti transfer,
 * Sertifikat, Cash & Carry) dengan ilustrasi, isinya sesuai prototype, dan
 * seluruh langkah tersedia sebagai teks (jadi terbaca di mobile maupun desktop
 * tanpa mengandalkan gambar).
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const penggunaCabang = {
  id: "u1",
  kind: "branch",
  role: "branch",
  nama: "Grahita Bandung",
  email: "bandung@example.test",
  branch_id: "b1",
};

function fetchPortal() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
}

function buatRouter() {
  return createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/tutorial"] }) });
}

const TOPIK = [
  "Cara memesan",
  "Cara unggah bukti transfer",
  "Aturan produk Sertifikat",
  "Pesanan Cash & Carry",
];

describe("Customer Portal — Tutorial (F1-19)", () => {
  it("menampilkan empat topik panduan, masing-masing dengan ilustrasi", async () => {
    fetchPortal();

    render(<RouterProvider router={buatRouter()} />);

    expect(await screen.findByRole("heading", { name: "Tutorial" })).toBeDefined();
    for (const judul of TOPIK) {
      // Judul topik adalah judul kartu (h2); accordion di bawahnya berlabel
      // "Langkah-langkah" supaya tidak mengulang judul yang sama.
      expect(screen.getByRole("heading", { name: judul })).toBeDefined();
    }
    expect(screen.getAllByText("Langkah-langkah")).toHaveLength(4);
    expect(screen.getAllByTestId("ilustrasi-topik")).toHaveLength(4);
    // Empat accordion (details) + topik pertama terbuka secara bawaan.
    const accordions = document.querySelectorAll("details");
    expect(accordions).toHaveLength(4);
    expect((accordions[0] as HTMLDetailsElement).open).toBe(true);
    expect((accordions[1] as HTMLDetailsElement).open).toBe(false);
  });

  it("memuat langkah-langkah sesuai aturan PRD (memesan, bukti, sertifikat, C&C)", async () => {
    fetchPortal();

    render(<RouterProvider router={buatRouter()} />);
    await screen.findByRole("heading", { name: "Tutorial" });

    const wajib = [
      // Cara memesan (PRD §3.5, CP-ORD-01..05)
      "Buka menu Pesanan lalu pilih “Buat pesanan”.",
      "Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”.",
      // Bukti transfer (PRD §3.5 CP-ORD-04, §3.7 CP-CNC-02)
      "Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.",
      "Nominal seluruh bukti dijumlahkan saat verifikasi Pusat.",
      // Sertifikat (PRD §3.6)
      "Harga satuan Sertifikat selalu Rp0 berapa pun jumlah lembarnya.",
      "Biaya packaging ditambahkan otomatis satu baris per pesanan dan tidak bisa diubah.",
      // Cash & Carry (PRD §3.7)
      "Jatuh tempo = tanggal pesanan + jumlah hari C&C yang berlaku (bawaan 7 hari).",
      "Bayar sebelum tanggal jatuh tempo; pesanan baru selesai setelah pembayaran diverifikasi Pusat.",
    ];
    for (const langkah of wajib) {
      expect(screen.getByText(langkah)).toBeDefined();
    }
    // Isi panduan adalah teks biasa: bisa dibaca pembaca layar tanpa gambar.
    expect(screen.getByText(/Panduan ini bisa dibaca di ponsel maupun desktop/)).toBeDefined();
  });
});
