import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

interface Panggilan {
  url: string;
  init?: RequestInit;
}

const PRODUK = [
  {
    id: "pr-1",
    kode: "P-01",
    nama: "Bio Magnetik",
    jenis: "reguler",
    harga_default: 350000,
    urutan: 1,
    is_active: true,
  },
  {
    id: "pr-2",
    kode: "P-02",
    nama: "Sertifikat Produk",
    jenis: "sertifikat",
    harga_default: 0,
    urutan: 2,
    is_active: false,
  },
];

const MATRIKS = {
  produk: PRODUK,
  cabang: [
    { id: "br-a", nama: "Grahita Cabang A", is_active: true },
    { id: "br-b", nama: "Grahita Cabang B", is_active: false },
  ],
  sel: [{ branch_id: "br-a", product_id: "pr-1", harga: 112500, hjm: 100000, p1: "0.1", spv: 2500, sumber: "import" }],
};

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

function stubFetch(tambahan?: (url: string, init?: RequestInit) => Response | undefined): Panggilan[] {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      const timpa = tambahan?.(input, init);
      if (timpa) return timpa;
      if (input.endsWith("/auth/me")) {
        return json({
          data: {
            user: { id: "a1", kind: "admin", role: "manager", nama: "Budi Santoso", email: "budi@example.test" },
          },
        });
      }
      if (input.includes("/admin/products") && (init?.method ?? "GET") === "GET") {
        return json({ data: PRODUK });
      }
      if (input.includes("/admin/prices") && (init?.method ?? "GET") === "GET") {
        return json({ data: MATRIKS });
      }
      if (input.endsWith("/admin/products")) return json({ data: { ...PRODUK[0], id: "pr-3" } }, 201);
      if (input.endsWith("/admin/prices")) return json({ data: { diubah: 1, dihapus: 0, diabaikan: 0 } });
      if (input.endsWith("/admin/prices/copy")) {
        return json({ data: { from_branch_id: "br-a", jumlah: 1, salinan: [{ branch_id: "br-b", jumlah: 1, diubah: 1 }] } });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

beforeEach(() => {
  document.cookie = "grahita_csrf=token-uji";
});

describe("Admin Portal — /katalog (F1-08)", () => {
  it("Tab Produk menampilkan produk termasuk yang nonaktif dan meneruskan filter jenis", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/katalog")} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    // Produk nonaktif tetap tampil di admin agar bisa diaktifkan kembali.
    expect(screen.getByText("Sertifikat Produk")).toBeDefined();
    // "Nonaktif" muncul sebagai badge produk sekaligus opsi filter status.
    expect(screen.getAllByText("Nonaktif").length).toBeGreaterThan(1);

    fireEvent.change(screen.getByLabelText("Jenis produk"), { target: { value: "sertifikat" } });

    await waitFor(() => {
      const panggil = panggilan.find((p) => p.url.includes("/admin/products?") && p.url.includes("jenis=sertifikat"));
      expect(panggil).toBeDefined();
    });
  });

  it("Tambah produk mengirim POST berisi field lengkap", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/katalog")} />);
    expect(await screen.findByText("Bio Magnetik")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah produk/ }));
    fireEvent.change(await screen.findByLabelText("Nama produk"), { target: { value: "Produk Baru" } });
    fireEvent.change(screen.getByLabelText("Kode"), { target: { value: "P-99" } });
    fireEvent.change(screen.getByLabelText("Harga default"), { target: { value: "12500" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    await waitFor(() => {
      const post = panggilan.find((p) => p.url.endsWith("/admin/products") && p.init?.method === "POST");
      expect(post).toBeDefined();
      expect(JSON.parse(String(post?.init?.body))).toEqual({
        kode: "P-99",
        nama: "Produk Baru",
        jenis: "reguler",
        harga_default: 12500,
        urutan: 0,
        is_active: true,
      });
      const header = post?.init?.headers as Record<string, string>;
      expect(header["X-CSRF-Token"]).toBe("token-uji");
    });
  });

  it("Memilih jenis Sertifikat mengunci harga default ke 0", async () => {
    stubFetch();

    render(<RouterProvider router={buatRouter("/katalog")} />);
    expect(await screen.findByText("Bio Magnetik")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah produk/ }));
    fireEvent.change(await screen.findByLabelText("Jenis"), { target: { value: "sertifikat" } });

    const harga = screen.getByLabelText("Harga default") as HTMLInputElement;
    expect(harga.disabled).toBe(true);
    expect(harga.value).toBe("0");
  });

  it("Tab Matriks menampilkan tarif beserta HJM · P1 · SPV dan mengirim komponen dari editor", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/katalog")} />);
    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Matriks harga" }));

    const selA = await screen.findByRole("button", { name: "Tarif Bio Magnetik untuk Grahita Cabang A" });
    expect(selA.textContent).toMatch(/112\.500/);
    expect(selA.textContent).toContain("100.000 · 10% · 2.500");
    // Cabang B belum dijual.
    expect(screen.getByRole("button", { name: "Tarif Bio Magnetik untuk Grahita Cabang B" }).textContent).toBe("Tidak dijual");

    fireEvent.click(selA);
    fireEvent.change(await screen.findByLabelText("HJM (Rp)"), { target: { value: "34100" } });
    fireEvent.change(screen.getByLabelText("P1 (%)"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("SPV (Rp)"), { target: { value: "2500" } });
    // Pratinjau memakai rumus server: 34.100 + 10% + 2.500 = 40.010.
    expect(screen.getByTestId("pratinjau-tarif").textContent).toMatch(/40\.010/);
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/prices") && p.init?.method === "PUT");
      expect(put).toBeDefined();
      // Harga jual tidak dikirim: server menghitungnya dari komponen.
      expect(JSON.parse(String(put?.init?.body))).toEqual({
        items: [{ branch_id: "br-a", product_id: "pr-1", hjm: 34100, p1: "0.1", spv: 2500 }],
      });
    });
  });

  it("Tidak dijual dari editor mengirim hjm null", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/katalog")} />);
    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Matriks harga" }));

    fireEvent.click(await screen.findByRole("button", { name: "Tarif Bio Magnetik untuk Grahita Cabang A" }));
    fireEvent.click(await screen.findByRole("button", { name: "Tidak dijual" }));

    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/prices") && p.init?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({
        items: [{ branch_id: "br-a", product_id: "pr-1", hjm: null }],
      });
    });
  });

  it("Salin harga ke semua cabang mengirim POST /admin/prices/copy", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/katalog")} />);
    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Matriks harga" }));
    await screen.findByRole("button", { name: "Tarif Bio Magnetik untuk Grahita Cabang A" });

    fireEvent.change(screen.getByLabelText("Salin harga dari"), { target: { value: "br-a" } });
    fireEvent.change(screen.getByLabelText("Ke"), { target: { value: "semua" } });
    fireEvent.click(screen.getByRole("button", { name: "Salin harga" }));

    await waitFor(() => {
      const post = panggilan.find((p) => p.url.endsWith("/admin/prices/copy") && p.init?.method === "POST");
      expect(post).toBeDefined();
      expect(JSON.parse(String(post?.init?.body))).toEqual({ from_branch_id: "br-a", semua: true });
    });
  });
});
