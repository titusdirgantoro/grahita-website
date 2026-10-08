import { render, screen, within } from "@testing-library/react";
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

/** fetchAndalan memasang fetch palsu untuk sesi cabang + katalog. */
function fetchAndalan() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/catalog")) {
        return json({
          data: [
            { id: "p1", nama: "Bio Magnetik", jenis: "reguler", satuan: "pcs", harga: 340000 },
            { id: "p7", nama: "Sertifikat Psikotes", jenis: "sertifikat", satuan: "lembar", harga: 0 },
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

    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    expect(within(bawah).getAllByRole("link")).toHaveLength(5);

    // Harga khusus cabang diformat Rupiah, sertifikat diberi badge.
    expect(screen.getByText(/Rp\s?340\.000/)).toBeDefined();
    expect(screen.getByText("Sertifikat")).toBeDefined();
  });

  it("rute /tutorial merender shell dan isi accordion", async () => {
    fetchAndalan();

    render(<RouterProvider router={buatRouter("/tutorial")} />);

    expect(await screen.findByText("Cara membuat dan mengirim pesanan")).toBeDefined();
    expect(screen.getByText("Aturan produk Sertifikat")).toBeDefined();
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
