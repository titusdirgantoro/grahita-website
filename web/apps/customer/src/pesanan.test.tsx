import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
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

const katalog = [
  { id: "p1", nama: "Bio Magnetik", jenis: "reguler", satuan: "pcs", harga: 100000 },
  {
    id: "p7",
    nama: "Sertifikat Psikotes",
    jenis: "sertifikat",
    satuan: "lembar",
    harga: 0,
    biaya_packaging: 3000,
  },
];

const draft = {
  id: "o1",
  status: "draft",
  verif_status: "belum",
  is_cnc: false,
  is_sertifikat: false,
  catatan: "dikirim pagi",
  total: 300000,
  jumlah_item: 3,
  created_at: "2026-10-07T02:00:00Z",
};

const draftLengkap = {
  ...draft,
  lines: [
    {
      line_no: 1,
      jenis: "product",
      product_id: "p1",
      nama: "Bio Magnetik",
      satuan: "pcs",
      qty: 3,
      harga_satuan: 100000,
      subtotal: 300000,
    },
  ],
};

const terkirim = {
  id: "o2",
  nomor: "2026-10-01",
  status: "submitted",
  verif_status: "belum",
  is_cnc: false,
  is_sertifikat: false,
  tanggal: "2026-10-08",
  catatan: "",
  total: 500000,
  jumlah_item: 5,
  created_at: "2026-10-08T01:00:00Z",
  lines: [
    {
      line_no: 1,
      jenis: "product",
      product_id: "p1",
      nama: "Bio Magnetik",
      satuan: "pcs",
      qty: 5,
      harga_satuan: 100000,
      subtotal: 500000,
      serial_number: "20261008001",
    },
  ],
};

interface Panggilan {
  url: string;
  init?: RequestInit;
}

/**
 * sertifikatCNC adalah pesanan Sertifikat ber-flag Cash & Carry dengan jatuh
 * tempo hasil hitungan server (tanggal submit WIB + cnc_hari) — dipakai untuk
 * memeriksa penanda C&C dan baris biaya packaging pada halaman detail.
 */
const sertifikatCNC = {
  id: "o3",
  status: "submitted",
  verif_status: "belum",
  is_cnc: true,
  is_sertifikat: true,
  nomor: "2026-10-02",
  tanggal: "2026-10-08",
  jatuh_tempo: "2026-10-15",
  catatan: "",
  total: 3000,
  jumlah_item: 10,
  created_at: "2026-10-08T01:00:00Z",
  lines: [
    {
      line_no: 1,
      jenis: "product",
      product_id: "p7",
      nama: "Sertifikat Psikotes",
      satuan: "lembar",
      qty: 10,
      harga_satuan: 0,
      subtotal: 0,
    },
    {
      line_no: 2,
      jenis: "packaging",
      nama: "Biaya packaging",
      satuan: "paket",
      qty: 1,
      harga_satuan: 3000,
      subtotal: 3000,
    },
  ],
};

/**
 * fetchPesanan memasang fetch palsu untuk portal cabang: sesi, katalog, dan
 * endpoint pesanan. Setiap panggilan dicatat supaya tes dapat memeriksa URL,
 * metode, dan body yang benar-benar dikirim UI.
 */
function fetchPesanan(opsi: { daftar?: unknown[]; detail?: unknown; bukti?: unknown[] } = {}) {
  const panggilan: Panggilan[] = [];
  const daftar = opsi.daftar ?? [draft, terkirim];
  const bukti = opsi.bukti ?? [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/catalog")) return json({ data: katalog });
      if (input.endsWith("/me/branch")) {
        return json({ data: { id: "b1", nama: "Grahita Bandung", nama_kepala: "Sri", email: "a@b.test", telepon: "", is_cnc: false, is_active: true } });
      }
      if (input.includes("/orders?status=draft")) {
        return json({ data: daftar.filter((o) => (o as { status: string }).status === "draft") });
      }
      if (input.endsWith("/orders") && (init?.method ?? "GET") === "GET") return json({ data: daftar });
      if (input.endsWith("/orders") && init?.method === "POST") {
        return json({ data: { ...draft, id: "o-baru" } }, 201);
      }
      if (input.endsWith("/orders/o1") && (init?.method ?? "GET") === "GET") {
        return json({ data: opsi.detail ?? draftLengkap });
      }
      if (input.endsWith("/orders/o1") && init?.method === "PUT") {
        return json({ data: { ...draftLengkap, total: 200000 } });
      }
      if (input.endsWith("/orders/o1") && init?.method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      if (input.endsWith("/orders/o2")) return json({ data: opsi.detail ?? terkirim });
      // Bukti transfer (F1-13): daftar bukti, unggah multipart, dan tautan unduh.
      if (input.endsWith("/proofs") && init?.method === "POST") {
        return json({ data: bukti }, 201);
      }
      if (input.endsWith("/proofs")) return json({ data: bukti });
      if (input.includes("/proofs/") && input.endsWith("/download")) {
        return json({ data: { url: "https://storage.test/bukti/abc.jpg?sig=1", expires_in: 300, nama_file: "transfer.jpg", mime_type: "image/jpeg" } });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

describe("Customer Portal — daftar pesanan (F1-11)", () => {
  it("menampilkan nomor, tanggal, item, total, dan status tiap pesanan", async () => {
    fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan")} />);

    // Draft belum punya nomor; pesanan yang sudah dikirim menampilkan nomornya.
    const tautanDraft = await screen.findByRole("link", { name: "Draft" });
    expect(tautanDraft.getAttribute("href")).toBe("/pesanan/o1");
    expect(screen.getByRole("link", { name: "2026-10-01" })).toBeDefined();
    expect(screen.getByText(/Dibuat/)).toBeDefined();
    expect(screen.getByText("Menunggu verifikasi")).toBeDefined();
    expect(screen.getByText(/300\.000/)).toBeDefined();
    expect(screen.getByText(/500\.000/)).toBeDefined();
  });

  it("memfilter daftar per status lewat URL dan API", async () => {
    const panggilan = fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan")} />);
    await screen.findByRole("link", { name: "Draft" });

    fireEvent.click(screen.getByRole("button", { name: "Draft" }));

    await waitFor(() => {
      expect(panggilan.some((p) => p.url.includes("/orders?status=draft"))).toBe(true);
    });
    // Hanya draft yang tersisa di tabel.
    expect(screen.queryByRole("link", { name: "2026-10-01" })).toBeNull();
  });
});

describe("Customer Portal — detail pesanan (F1-11)", () => {
  it("menampilkan baris, total, catatan, dan aksi draft", async () => {
    fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/o1")} />);

    expect(await screen.findByRole("heading", { name: "Draft" })).toBeDefined();
    expect(screen.getByText("Bio Magnetik")).toBeDefined();
    expect(screen.getByText("dikirim pagi")).toBeDefined();
    expect(screen.getByRole("button", { name: "Ubah" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Hapus draft" })).toBeDefined();
    // Draft tidak menampilkan kolom serial number.
    expect(screen.queryByRole("columnheader", { name: "Serial number" })).toBeNull();
  });

  it("menghapus draft setelah konfirmasi modal", async () => {
    const panggilan = fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/o1")} />);
    await screen.findByRole("heading", { name: "Draft" });

    fireEvent.click(screen.getByRole("button", { name: "Hapus draft" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Hapus draft" }));

    await waitFor(() => {
      const hapus = panggilan.find((p) => p.init?.method === "DELETE");
      expect(hapus).toBeDefined();
      expect(hapus?.url.endsWith("/orders/o1")).toBe(true);
    });
  });

  it("mengunci pesanan yang sudah dikirim: tanpa tombol ubah/hapus", async () => {
    fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/o2")} />);

    expect(await screen.findByRole("heading", { name: "2026-10-01" })).toBeDefined();
    expect(screen.queryByRole("button", { name: "Ubah" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Hapus draft" })).toBeNull();
    expect(screen.getByText(/sudah dikirim/)).toBeDefined();
    // Pesanan yang sudah dikirim menampilkan serial number.
    expect(screen.getByRole("columnheader", { name: "Serial number" })).toBeDefined();
    expect(screen.getByText("20261008001")).toBeDefined();
  });
});

describe("Customer Portal — editor pesanan (F1-11)", () => {
  it("menghitung subtotal & total live dari harga cabang lalu menyimpan draft", async () => {
    const panggilan = fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/baru")} />);

    const pilih = await screen.findByLabelText("Produk baris 1");
    fireEvent.change(pilih, { target: { value: "p1" } });

    // Harga satuan datang dari katalog cabang, bukan diisi pengguna.
    const subtotalAwal = await screen.findByTestId("total-draft");
    expect(subtotalAwal.textContent).toMatch(/100\.000/);

    fireEvent.click(screen.getByRole("button", { name: "Tambah qty baris 1" }));
    expect((screen.getByLabelText("Qty baris 1") as HTMLInputElement).value).toBe("2");
    expect(screen.getByTestId("total-draft").textContent).toMatch(/200\.000/);

    fireEvent.change(screen.getByLabelText("Catatan (opsional)"), {
      target: { value: "dikirim pagi" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan draft" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.init?.method === "POST" && p.url.endsWith("/orders"));
      expect(kirim).toBeDefined();
      // HANYA product_id dan qty yang dikirim: harga/total dihitung server.
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({
        catatan: "dikirim pagi",
        lines: [{ product_id: "p1", qty: 2 }],
      });
    });
  });

  it("menolak menyimpan draft tanpa produk", async () => {
    const panggilan = fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/baru")} />);
    await screen.findByLabelText("Produk baris 1");

    fireEvent.click(screen.getByRole("button", { name: "Simpan draft" }));

    expect(await screen.findByText("Pilih minimal satu produk sebelum menyimpan draft.")).toBeDefined();
    expect(panggilan.some((p) => p.init?.method === "POST")).toBe(false);
  });

  it("menandai Sertifikat dipesan terpisah dan mengunci tambah baris", async () => {
    fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/baru")} />);
    const pilih = await screen.findByLabelText("Produk baris 1");
    fireEvent.change(pilih, { target: { value: "p7" } });

    expect(await screen.findByText(/Sertifikat dipesan terpisah/)).toBeDefined();
    expect((screen.getByRole("button", { name: "Tambah produk" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it("memuat draft yang diubah dan mengirim ulang hanya product_id + qty", async () => {
    const panggilan = fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/o1/ubah")} />);

    const pilih = (await screen.findByLabelText("Produk baris 1")) as HTMLSelectElement;
    await waitFor(() => expect(pilih.value).toBe("p1"));
    expect((screen.getByLabelText("Catatan (opsional)") as HTMLTextAreaElement).value).toBe(
      "dikirim pagi",
    );

    fireEvent.change(screen.getByLabelText("Qty baris 1"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan draft" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.init?.method === "PUT");
      expect(kirim?.url.endsWith("/orders/o1")).toBe(true);
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({
        catatan: "dikirim pagi",
        lines: [{ product_id: "p1", qty: 4 }],
      });
    });
  });

  it("menampilkan baris biaya packaging terkunci dan ikut menghitung total", async () => {
    fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/baru")} />);
    const pilih = await screen.findByLabelText("Produk baris 1");
    fireEvent.change(pilih, { target: { value: "p7" } });

    // Baris biaya packaging ditampilkan sebagai baris terkunci: dibuat server,
    // tidak bisa diubah atau dihapus dari editor (PRD CP-SER-02).
    const pack = await screen.findByTestId("baris-packaging");
    expect(within(pack).getByText("Biaya packaging")).toBeDefined();
    expect(within(pack).getAllByText(/3\.000/).length).toBeGreaterThan(0);
    expect(within(pack).queryByRole("button")).toBeNull();
    expect(within(pack).queryByRole("checkbox")).toBeNull();

    // Total editor sudah termasuk biaya packaging (0 × qty + 3.000).
    expect(screen.getByTestId("total-draft").textContent).toMatch(/3\.000/);
    expect(await screen.findByText(/ditambahkan otomatis per pesanan/)).toBeDefined();
  });

  it("tidak menampilkan baris packaging untuk pesanan reguler", async () => {
    fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/baru")} />);
    const pilih = await screen.findByLabelText("Produk baris 1");
    fireEvent.change(pilih, { target: { value: "p1" } });

    await screen.findByTestId("total-draft");
    expect(screen.queryByTestId("baris-packaging")).toBeNull();
  });
});

describe("Customer Portal — Sertifikat & Cash and Carry (F1-12)", () => {
  it("menandai C&C, menampilkan jatuh tempo, dan mengunci baris packaging di detail", async () => {
    fetchPesanan({ detail: sertifikatCNC });

    render(<RouterProvider router={buatRouter("/pesanan/o1")} />);

    expect(await screen.findByRole("heading", { name: "2026-10-02" })).toBeDefined();
    // Penanda C&C (PRD CP-CNC-03) dan jatuh tempo hasil hitungan server.
    expect(screen.getByText("Cash & Carry")).toBeDefined();
    expect(screen.getByText("Sertifikat")).toBeDefined();
    expect(screen.getByText("Jatuh tempo C&C")).toBeDefined();
    expect(screen.getByText("15 Okt 2026")).toBeDefined();
    // Baris biaya packaging tampil di rincian dengan harga snapshot-nya.
    expect(screen.getByText("Biaya packaging")).toBeDefined();
    expect(screen.getAllByText(/3\.000/).length).toBeGreaterThan(0);
    // Pesanan yang sudah dikirim tetap terkunci untuk cabang.
    expect(screen.queryByRole("button", { name: "Ubah" })).toBeNull();
  });
});

describe("Customer Portal — bukti transfer (F1-13)", () => {
  const buktiDua = [
    {
      id: "pf1",
      order_id: "o2",
      nama_file: "transfer-cicilan-1.jpg",
      mime_type: "image/jpeg",
      ukuran_byte: 204800,
      sha256: "a".repeat(64),
      uploaded_by_kind: "branch",
      created_at: "2026-10-08T02:00:00Z",
    },
    {
      id: "pf2",
      order_id: "o2",
      nama_file: "koreksi-admin.pdf",
      mime_type: "application/pdf",
      ukuran_byte: 1024,
      sha256: "b".repeat(64),
      uploaded_by_kind: "admin",
      uploaded_by_nama: "Budi",
      created_at: "2026-10-08T03:00:00Z",
    },
  ];

  it("menampilkan bukti beserta penanda pengunggah cabang/admin", async () => {
    fetchPesanan({ bukti: buktiDua });

    render(<RouterProvider router={buatRouter("/pesanan/o2")} />);

    expect(await screen.findByRole("heading", { name: "Bukti transfer" })).toBeDefined();
    // Baris bukti dimuat lewat permintaan terpisah dari detail pesanan, jadi
    // ditunggu dulu — bukan diasumsikan sudah ada saat heading muncul.
    expect(await screen.findByText("transfer-cicilan-1.jpg")).toBeDefined();
    expect(screen.getByText("koreksi-admin.pdf")).toBeDefined();
    // Unggahan cabang tidak bernama; unggahan admin menandai namanya (AP-ORD-06).
    expect(screen.getByText("Cabang")).toBeDefined();
    expect(screen.getByText("Admin Budi")).toBeDefined();
    expect(screen.getByText("200 KB")).toBeDefined();
  });

  it("pratinjau memakai tautan unduh berumur pendek dan bisa di-zoom", async () => {
    const panggilan = fetchPesanan({ bukti: buktiDua });

    render(<RouterProvider router={buatRouter("/pesanan/o2")} />);
    await screen.findByText("transfer-cicilan-1.jpg");

    fireEvent.click(screen.getAllByRole("button", { name: "Lihat" })[0]);

    expect(await screen.findByTestId("pratinjau-bukti")).toBeDefined();
    expect(panggilan.some((p) => p.url.endsWith("/proofs/pf1/download"))).toBe(true);
    const gambar = screen.getByTestId("pratinjau-bukti");
    expect(gambar.getAttribute("src")).toContain("?sig=1");

    // Klik gambar menyalakan zoom (kelas berubah).
    const kelasAwal = gambar.className;
    fireEvent.click(gambar);
    expect(screen.getByTestId("pratinjau-bukti").className).not.toBe(kelasAwal);
  });

  it("mengunggah berkas bukti sebagai multipart ke endpoint proofs", async () => {
    const panggilan = fetchPesanan({ bukti: [] });

    render(<RouterProvider router={buatRouter("/pesanan/o2")} />);
    await screen.findByTestId("bukti-kosong");

    const berkas = new File(["data"], "transfer.jpg", { type: "image/jpeg" });
    fireEvent.change(screen.getByLabelText(/Tambah bukti/), { target: { files: [berkas] } });
    fireEvent.click(screen.getByRole("button", { name: "Unggah bukti" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.init?.method === "POST" && p.url.endsWith("/orders/o2/proofs"));
      expect(kirim).toBeDefined();
    });
    const kirim = panggilan.find((p) => p.init?.method === "POST" && p.url.endsWith("/orders/o2/proofs"));
    expect(kirim?.init?.body instanceof FormData).toBe(true);
    const form = kirim?.init?.body as FormData;
    expect(form.getAll("file").length).toBe(1);
    // Content-Type TIDAK disetel manual supaya boundary multipart tetap sah.
    const headers = kirim?.init?.headers as Record<string, string>;
    expect(headers["Content-Type"]).toBeUndefined();
  });

  it("pesanan selesai tidak menampilkan form unggah", async () => {
    fetchPesanan({ detail: { ...terkirim, status: "done" }, bukti: buktiDua });

    render(<RouterProvider router={buatRouter("/pesanan/o2")} />);
    await screen.findByRole("heading", { name: "Bukti transfer" });

    expect(screen.queryByRole("button", { name: "Unggah bukti" })).toBeNull();
    expect(screen.queryByLabelText(/Tambah bukti/)).toBeNull();
  });

  it("draft tidak punya bagian bukti sama sekali", async () => {
    const panggilan = fetchPesanan();

    render(<RouterProvider router={buatRouter("/pesanan/o1")} />);
    await screen.findByRole("heading", { name: "Draft" });

    expect(screen.queryByRole("heading", { name: "Bukti transfer" })).toBeNull();
    expect(panggilan.some((p) => p.url.includes("/proofs"))).toBe(false);
  });
});
