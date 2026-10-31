import { fireEvent, render, screen, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { NAV_ADMIN } from "./nav";
import { routeTree } from "./routes";

function router(path: string) {
  return createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
}

function session() {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith("/auth/me")) {
      return new Response(JSON.stringify({ data: { user: { id: "admin-1", kind: "admin", role: "superadmin", nama: "Rina", email: "rina@example.test" } } }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    return new Response(JSON.stringify({ error: { code: "NOT_FOUND", message: "tidak ada" } }), { status: 404, headers: { "Content-Type": "application/json" } });
  }));
}

describe("Dokumentasi portal admin", () => {
  it("menempatkan Dokumentasi setelah Audit log dan memuat seluruh 162 skenario", async () => {
    const lastGroup = NAV_ADMIN.at(-1)?.items ?? [];
    expect(lastGroup.slice(-2).map((item) => item.label)).toEqual(["Audit log", "Dokumentasi"]);
    session();
    render(<RouterProvider router={router("/dokumentasi")} />);
    expect(await screen.findByRole("heading", { name: "Katalog 162 skenario end to end" }, { timeout: 15000 })).toBeDefined();
    expect(screen.getByRole("status").textContent).toContain("162 dari 162");
    expect(screen.getAllByRole("figure", { name: /Diagram alur/ })).toHaveLength(9);
    expect(screen.getByRole("figure", { name: "Diagram alur 6. Jalur Tempo dan pelunasan" })).toBeDefined();
    const tab = screen.getByRole("navigation", { name: "Jenis dokumentasi" });
    expect(within(tab).getByRole("link", { name: "Panduan Penggunaan" }).getAttribute("aria-current")).toBe("page");
    fireEvent.change(screen.getByRole("searchbox", { name: "Cari skenario" }), { target: { value: "TMP-22" } });
    expect(screen.getByRole("status").textContent).toContain("1 dari 162");
    expect(screen.getByText("TMP-22")).toBeDefined();
  });

  it("menyediakan diagram arsitektur dan relasi data pada rute teknis", async () => {
    session();
    render(<RouterProvider router={router("/dokumentasi/teknis")} />);
    expect(await screen.findByRole("heading", { name: "Arsitektur sistem yang berjalan" }, { timeout: 15000 })).toBeDefined();
    expect(screen.getByRole("img", { name: /Portal cabang dan admin melalui Caddy/ })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Model data dan relasi inti" })).toBeDefined();
    expect(screen.getAllByRole("img", { name: /Diagram UML database/ })).toHaveLength(5);
    expect(screen.getByRole("img", { name: /Diagram UML database Piutang Tempo dan Surat Tagihan/ })).toBeDefined();
    expect(screen.getAllByText(/0\.\.\*/).length).toBeGreaterThan(0);
    const tab = screen.getByRole("navigation", { name: "Jenis dokumentasi" });
    expect(within(tab).getByRole("link", { name: "Dokumentasi Teknis" }).getAttribute("aria-current")).toBe("page");
  });
});
