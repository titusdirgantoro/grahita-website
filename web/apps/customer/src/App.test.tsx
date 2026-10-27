import { fireEvent, render, screen, within } from "@testing-library/react";
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
};

/**
 * fetchAndalan memasang fetch palsu untuk sesi cabang + katalog. `cnc` adalah
 * flag pembayaran dan riwayat Tempo cabang sesi (menentukan menu terkait).
 */
function fetchAndalan(cnc = true, tempo = false, tempoHistory = false) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/me/branch")) return json({ data: {
        id: "b1", nama: "Grahita Bandung", is_cnc: cnc,
        is_tempo: tempo, has_tempo_history: tempoHistory,
      } });
      if (input.endsWith("/catalog")) {
        return json({
          data: [
            { id: "p1", nama: "Bio Magnetik", jenis: "reguler", harga: 340000 },
            {
              id: "p7",
              nama: "Sertifikat Psikotes",
              jenis: "sertifikat",

              harga: 0,
              biaya_packaging: 3000,
            },
          ],
        });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
}

describe("Customer Portal — shell & navigasi", () => {
  it("rute /masuk memakai layout auth (tanpa sidebar) saat belum login", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ error: { code: "UNAUTHENTICATED", message: "belum masuk" } }, 401)),
    );

    render(<RouterProvider router={buatRouter("/masuk")} />);

    expect(await screen.findByText("Masuk ke portal cabang")).toBeDefined();
    expect(screen.getByLabelText("Email cabang")).toBeDefined();
    // Layout auth tidak memuat navigasi shell.
    expect(screen.queryByRole("navigation", { name: "Navigasi bawah" })).toBeNull();
  });

  it("rute ber-shell menampilkan sidebar, bottom-nav, dan katalog cabang", async () => {
    fetchAndalan();

    render(<RouterProvider router={buatRouter("/katalog")} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();

    const sidebar = screen.getByRole("complementary");
    const nav = within(sidebar).getByRole("navigation");
    expect(within(nav).getByRole("link", { name: /Beranda/ })).toBeDefined();
    expect(within(nav).getByRole("link", { name: /Riwayat invoice/ })).toBeDefined();
    expect(within(nav).queryByRole("link", { name: /Profil/ })).toBeNull();
    expect(within(nav).getByRole("link", { name: /Katalog/ }).getAttribute("aria-current")).toBe("page");

    // Di layar sempit sidebar tersembunyi: Deposit (cabang C&C) hanya
    // terjangkau lewat bottom-nav, sedangkan Tutorial tetap lewat kartu
    // panduan di Beranda.
    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    expect(await within(bawah).findByRole("link", { name: /Deposit/ })).toBeDefined();
    expect(within(bawah).queryByRole("link", { name: /Tutorial/ })).toBeNull();

    // Harga khusus cabang diformat Rupiah, sertifikat diberi badge.
    expect(screen.getByText(/Rp\s?340\.000/)).toBeDefined();
    expect(screen.getByText("Sertifikat")).toBeDefined();
    // CP-KAT-04: produk Sertifikat mencantumkan biaya packaging yang berlaku.
    expect(screen.getByText(/Biaya packaging Rp\s?3\.000 ditambahkan otomatis per pesanan\./)).toBeDefined();
  });

  it("menu Deposit hanya untuk cabang Cash & Carry", async () => {
    fetchAndalan(false);

    render(<RouterProvider router={buatRouter("/katalog")} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    expect(within(bawah).getByRole("link", { name: /Pesanan/ })).toBeDefined();
    expect(within(bawah).queryByRole("link", { name: /Deposit/ })).toBeNull();
  });

  it("menu Tagihan tetap ada setelah izin Tempo dicabut bila ada riwayat pesanan", async () => {
    fetchAndalan(false, false, true);

    render(<RouterProvider router={buatRouter("/katalog")} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    const sidebar = within(screen.getByRole("complementary")).getByRole("navigation");
    expect(within(sidebar).getByRole("link", { name: /Tagihan/ })).toBeDefined();
    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    fireEvent.click(within(bawah).getByRole("link", { name: /Tagihan/ }));
    expect(await screen.findByRole("heading", { name: "Tagihan" })).toBeDefined();
  });

  it("cabang Deposit dengan riwayat Tempo tetap menemukan kedua menu pembayaran", async () => {
    fetchAndalan(true, false, true);

    render(<RouterProvider router={buatRouter("/katalog")} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    expect(within(bawah).getByRole("link", { name: /Deposit/ })).toBeDefined();
    expect(within(bawah).getByRole("link", { name: /Tagihan/ })).toBeDefined();
    expect(within(bawah).queryByRole("link", { name: /^Invoice$/ })).toBeNull();
  });

  it("menu Tagihan tidak muncul untuk cabang yang belum pernah memakai Tempo", async () => {
    fetchAndalan(false, false, false);

    render(<RouterProvider router={buatRouter("/katalog")} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    expect(within(screen.getByRole("complementary")).queryByRole("link", { name: /Tagihan/ })).toBeNull();
  });

  it("rute /tutorial merender shell dan empat topik panduan", async () => {
    fetchAndalan();

    render(<RouterProvider router={buatRouter("/tutorial")} />);

    // Judul topik muncul sebagai judul kartu (h2) dan ringkasan accordion.
    await screen.findByRole("heading", { name: "Cara memesan" });
    expect(screen.getAllByText("Aturan produk Sertifikat").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Pesanan Cash & Carry").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByRole("complementary")).toBeDefined();
  });

  it("halaman /gaya merender shell + seluruh komponen tanpa memanggil API", async () => {
    const fetchPalau = vi.fn(async (_input: string, _init?: RequestInit) =>
      json({ error: { code: "UNAUTHENTICATED", message: "belum masuk" } }, 401),
    );
    vi.stubGlobal("fetch", fetchPalau);

    render(<RouterProvider router={buatRouter("/gaya")} />);

    expect(await screen.findByText("Sistem desain")).toBeDefined();
    expect(screen.getByRole("complementary")).toBeDefined();
    expect(screen.getByText("Tombol")).toBeDefined();
    expect(screen.getByText("Status & badge")).toBeDefined();
    // Gallery memakai akun contoh: satu-satunya panggilan API adalah pemeriksaan
    // sesi di root route, tidak ada pengambilan katalog.
    expect(fetchPalau.mock.calls.map((c) => c[0])).toEqual(["/api/v1/auth/me"]);
  });
});
