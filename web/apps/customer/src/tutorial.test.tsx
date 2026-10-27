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
      "Pilih cara pembayaran yang tersedia untuk cabang Anda: Transfer di muka, Deposit (Cash & Carry), atau Tempo.",
      "Tekan “Kirim pesanan”. Invoice tersedia setelah pesanan disetujui dan dokumen terbit.",
      // Bukti transfer (PRD §3.5 CP-ORD-04, §3.7 CP-CNC-02)
      "Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.",
      "Nominal seluruh bukti dijumlahkan saat verifikasi Pusat.",
      "Bukti saat mengirim pesanan wajib untuk Transfer di muka. Deposit memerlukan bukti bila saldo kurang; Tempo tidak memerlukan bukti di muka.",
      // Sertifikat (PRD §3.6)
      "Harga satuan Sertifikat selalu Rp0 berapa pun jumlah lembarnya.",
      "Biaya packaging ditambahkan otomatis satu baris per pesanan dan tidak bisa diubah.",
      // Cash & Carry (PRD §3.7)
      "Saat pesanan dikirim, saldo deposit langsung dipotong sebesar total pesanan.",
      "Bila saldo kurang, transfer sisanya dan unggah buktinya saat mengirim pesanan.",
    ];
    for (const langkah of wajib) {
      expect(screen.getByText(langkah)).toBeDefined();
    }
    // Isi panduan adalah teks biasa: bisa dibaca pembaca layar tanpa gambar.
    expect(screen.getByText(/Panduan ini bisa dibaca di ponsel maupun desktop/)).toBeDefined();
  });
});

/**
 * Uji konten dinamis (kartu F2-27): isi halaman datang dari
 * `GET /api/v1/tutorial`, sehingga perubahan konten oleh Superadmin langsung
 * terlihat saat portal di-refresh — tanpa build/deploy — dan konten berbahaya
 * tidak pernah menjadi markup di halaman.
 */

/** fetchTutorial memasang fetch palsu: sesi cabang + konten tutorial tertentu. */
function fetchTutorial(payload: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/tutorial")) return json(payload);
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
}

describe("Customer Portal — Tutorial dinamis (F2-27)", () => {
  it("menampilkan konten versi server apa adanya (tanpa build/deploy)", async () => {
    fetchTutorial({
      data: {
        topik: [
          {
            id: "t1",
            ikon: "memesan",
            judul: "Cara memesan (versi baru)",
            ringkas: "Ringkasan baru dari Pusat.",
            langkah: ["Langkah baru pertama.", "Langkah baru kedua."],
          },
          {
            id: "t2",
            ikon: "ikon-tak-dikenal",
            judul: "Topik tambahan",
            ringkas: "Ringkas tambahan.",
            langkah: ["Satu langkah."],
          },
        ],
      },
    });

    render(<RouterProvider router={buatRouter()} />);

    // Judul dan langkah versi server menggantikan konten bawaan.
    expect(await screen.findByRole("heading", { name: "Cara memesan (versi baru)" })).toBeDefined();
    expect(screen.getByText("Ringkasan baru dari Pusat.")).toBeDefined();
    expect(screen.getByText("Langkah baru kedua.")).toBeDefined();
    expect(screen.getByText("Topik tambahan")).toBeDefined();
    // Empat topik bawaan tidak lagi ditampilkan seluruhnya.
    expect(screen.queryByRole("heading", { name: "Cara memesan" })).toBeNull();
    expect(screen.queryByText("Aturan produk Sertifikat")).toBeNull();
    // Setiap topik tetap punya ilustrasi bawaan aplikasi (kunci tak dikenal
    // memakai ilustrasi umum), dan topik pertama terbuka.
    expect(screen.getAllByTestId("ilustrasi-topik")).toHaveLength(2);
    const accordions = document.querySelectorAll("details");
    expect(accordions).toHaveLength(2);
    expect((accordions[0] as HTMLDetailsElement).open).toBe(true);
  });

  it("menampilkan konten berbahaya sebagai teks, bukan HTML tersuntik", async () => {
    fetchTutorial({
      data: {
        topik: [
          {
            id: "t1",
            ikon: "umum",
            judul: "Judul <b>tebal</b>",
            ringkas: "Ringkas <script>alert(1)</script>aman.",
            langkah: ["<img src=x onerror=alert(1)>Baik", "Kedua"],
          },
        ],
      },
    });

    render(<RouterProvider router={buatRouter()} />);
    await screen.findByRole("heading", { name: "Judul <b>tebal</b>" });

    // Teks ditampilkan apa adanya (ter-escape React): tidak ada elemen yang
    // lahir dari konten, dan tidak ada skrip yang berjalan.
    expect(screen.getByText("<img src=x onerror=alert(1)>Baik")).toBeDefined();
    const kartu = screen.getByRole("heading", { name: "Judul <b>tebal</b>" }).closest("section");
    expect(kartu).not.toBeNull();
    expect(kartu?.querySelector("img")).toBeNull();
    expect(kartu?.querySelector("script")).toBeNull();
    expect(screen.getByRole("heading", { name: "Judul <b>tebal</b>" }).querySelector("b")).toBeNull();
  });

  it("tetap menampilkan empat topik bawaan bila permintaan gagal", async () => {
    fetchPortal();

    render(<RouterProvider router={buatRouter()} />);
    await screen.findByRole("heading", { name: "Tutorial" });

    expect(screen.getAllByTestId("ilustrasi-topik")).toHaveLength(4);
    expect(screen.getByRole("heading", { name: "Pesanan Cash & Carry" })).toBeDefined();
  });
});
