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

function fetchAdmin(role: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      if (input.endsWith("/auth/me")) {
        return json({
          data: { user: { id: "a1", kind: "admin", role, nama: "Rina Kartika", email: "rina@example.test" } },
        });
      }
      if (input.endsWith("/admin/branches")) {
        return json({
          data: [
            {
              id: "b1",
              nama: "Grahita Bandung",
              nama_kepala: "Sri Wahyuni",
              email: "bandung@example.test",
              is_cnc: true,
              is_active: true,
            },
          ],
        });
      }
      if (input.endsWith("/admin/settings")) {
        return json({ data: { biaya_packaging: 3000, cnc_hari: 7 } });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
}

describe("Admin Portal — shell, guard peran, & navigasi", () => {
  it("rute /masuk menampilkan form masuk admin", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ error: { code: "UNAUTHENTICATED", message: "belum masuk" } }, 401)),
    );

    render(<RouterProvider router={buatRouter("/masuk")} />);

    expect(await screen.findByText("Masuk ke admin portal")).toBeDefined();
    expect(screen.getByLabelText("Email")).toBeDefined();
  });

  it("Superadmin melihat seluruh menu dan tabel cabang", async () => {
    fetchAdmin("superadmin");

    render(<RouterProvider router={buatRouter("/cabang")} />);

    expect(await screen.findByText("Grahita Bandung")).toBeDefined();
    const sidebar = screen.getByRole("complementary");
    expect(within(sidebar).getByRole("link", { name: /Rekening bank/ })).toBeDefined();
    expect(within(sidebar).getByRole("link", { name: /Reporting/ })).toBeDefined();
    expect(within(sidebar).getByRole("link", { name: /Pengaturan/ })).toBeDefined();
    expect(screen.getByText("Superadmin")).toBeDefined();
    expect(screen.getAllByText("Aktif").length).toBeGreaterThanOrEqual(1);
  });

  it("Manager kehilangan menu Superadmin", async () => {
    fetchAdmin("manager");

    render(<RouterProvider router={buatRouter("/cabang")} />);

    expect(await screen.findByText("Grahita Bandung")).toBeDefined();
    const sidebar = screen.getByRole("complementary");
    expect(within(sidebar).queryByRole("link", { name: /Rekening bank/ })).toBeNull();
    expect(within(sidebar).queryByRole("link", { name: /Pengaturan/ })).toBeNull();
    expect(screen.getByText("Manager")).toBeDefined();
  });

  it("halaman /pengaturan menolak akses Manager (guard rute)", async () => {
    fetchAdmin("manager");

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    expect(await screen.findByText("Tidak berwenang")).toBeDefined();
    expect(screen.queryByText("Nilai tersimpan")).toBeNull();
  });

  it("halaman /pengaturan menampilkan nilai pengaturan untuk Superadmin", async () => {
    fetchAdmin("superadmin");

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    expect(await screen.findByText("Nilai tersimpan")).toBeDefined();
    expect(await screen.findByText("biaya_packaging")).toBeDefined();
  });

  it("halaman /gaya merender shell + komponen tanpa memanggil API", async () => {
    const fetchPalau = vi.fn(async (_input: string, _init?: RequestInit) =>
      json({ error: { code: "UNAUTHENTICATED", message: "belum masuk" } }, 401),
    );
    vi.stubGlobal("fetch", fetchPalau);

    render(<RouterProvider router={buatRouter("/gaya")} />);

    expect(await screen.findByText("Sistem desain")).toBeDefined();
    expect(screen.getByRole("complementary")).toBeDefined();
    expect(screen.getByRole("button", { name: "Buka menu" })).toBeDefined();
    // Gallery memakai akun contoh: hanya pemeriksaan sesi di root route.
    expect(fetchPalau.mock.calls.map((c) => c[0])).toEqual(["/api/v1/auth/me"]);
  });
});
