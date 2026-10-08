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
  { id: "p7", nama: "Sertifikat Psikotes", jenis: "sertifikat", satuan: "lembar", harga: 0 },
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
 * fetchPesanan memasang fetch palsu untuk portal cabang: sesi, katalog, dan
 * endpoint pesanan. Setiap panggilan dicatat supaya tes dapat memeriksa URL,
 * metode, dan body yang benar-benar dikirim UI.
 */
function fetchPesanan(opsi: { daftar?: unknown[]; detail?: unknown } = {}) {
  const panggilan: Panggilan[] = [];
  const daftar = opsi.daftar ?? [draft, terkirim];
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
      if (input.endsWith("/orders/o2")) return json({ data: terkirim });
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
});
