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

const AUDIT = [
  {
    id: "11111111-1111-4111-8111-111111111111",
    at: "2026-10-08T09:30:00Z",
    actor_kind: "admin",
    actor_id: "22222222-2222-4222-8222-222222222222",
    actor_nama: "Rina",
    ip: "203.0.113.9",
    entity: "order",
    entity_id: "33333333-3333-4333-8333-333333333333",
    action: "revise",
    before: { versi: 1, total: 100000, token: "[REDACTED]" },
    after: { versi: 2, total: 125000, delta_total: 25000, alasan: "Harga naik dari pusat" },
  },
  {
    id: "44444444-4444-4444-8444-444444444444",
    at: "2026-10-08T03:00:00Z",
    actor_kind: "admin",
    actor_id: "55555555-5555-4555-8555-555555555555",
    actor_nama: "Budi",
    ip: "203.0.113.10",
    entity: "admin_user",
    entity_id: "66666666-6666-4666-8666-666666666666",
    action: "update",
    before: { nama: "Dewi", role: "manager" },
    after: { nama: "Dewi", role: "superadmin" },
  },
];

/** halamanAudit menyusun respons GET /admin/audit-logs yang konsisten. */
function halamanAudit(url: string) {
  const q = new URL(url, "http://uji.local").searchParams;
  const halaman = Number(q.get("halaman") ?? "1");
  const perHalaman = Number(q.get("per_halaman") ?? "25");
  const entitas = q.get("entitas");
  const items = entitas ? AUDIT.filter((a) => a.entity === entitas) : AUDIT;
  // Tanpa filter, total sengaja lebih besar dari jumlah baris contoh supaya
  // paginasi (tombol "Berikutnya") benar-benar bisa diuji.
  const total = entitas ? items.length : 60;
  return {
    items: halaman === 1 ? items : [],
    total,
    halaman,
    per_halaman: perHalaman,
    total_halaman: Math.max(1, Math.ceil(total / perHalaman)),
    filter: { entitas: entitas ? [entitas] : [], halaman, per_halaman: perHalaman },
  };
}

function stubFetch(role: string): Panggilan[] {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) {
        return json({
          data: { user: { id: "a1", kind: "admin", role, nama: "Rina Kartika", email: "rina@example.test" } },
        });
      }
      if (input.includes("/admin/audit-logs")) return json({ data: halamanAudit(input) });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

beforeEach(() => {
  document.cookie = "grahita_csrf=token-uji";
});

describe("Admin Portal — /audit (F2-28)", () => {
  it("Superadmin melihat audit log beserta aktor, diff, dan ringkasan revisi", async () => {
    stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/audit")} />);

    // Pager hanya menampilkan angka sebenarnya setelah data termuat.
    expect(await screen.findByText(/Halaman 1 dari 3 · 60 entri/)).toBeDefined();
    expect(screen.getAllByText("Rina").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Budi").length).toBeGreaterThan(0);
    // Waktu ditampilkan dalam zona WIB (2026-10-08T09:30Z → 16:30 WIB).
    expect(screen.getAllByText(/8 Okt 2026/).length).toBeGreaterThan(0);
    expect(screen.getByText("203.0.113.9")).toBeDefined();

    // Diff baru terlihat setelah tombol ditekan; nilainya sudah teredaksi.
    expect(screen.queryByText(/"versi": 1/)).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: "Lihat diff" })[0]);

    const before = await screen.findByText(/"versi": 1/);
    expect(before.textContent).toContain("[REDACTED]");
    expect(screen.getByText(/"delta_total": 25000/)).toBeDefined();
    // Ringkasan khusus revisi: alasan + delta nilai (bukan hanya JSON mentah).
    expect(screen.getByText("Alasan: Harga naik dari pusat")).toBeDefined();
    expect(screen.getByText(/Δ nilai: Rp\s?25\.000/)).toBeDefined();
  });

  it("Filter entitas memakai URL search param dan mengirimnya ke API", async () => {
    const panggilan = stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/audit")} />);
    expect(await screen.findByText(/Halaman 1 dari 3 · 60 entri/)).toBeDefined();
    expect(screen.getByText("Budi")).toBeDefined();

    fireEvent.change(screen.getByLabelText("Entitas"), { target: { value: "order" } });

    await waitFor(() => {
      expect(panggilan.some((p) => p.url.includes("/admin/audit-logs") && p.url.includes("entitas=order"))).toBe(
        true,
      );
    });
    // Baris akun admin tersaring keluar oleh filter entitas (aktornya hilang).
    await waitFor(() => expect(screen.queryByText("Budi")).toBeNull());
  });

  it("Paginasi memuat halaman berikutnya tanpa kehilangan informasi total", async () => {
    const panggilan = stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/audit")} />);
    expect(await screen.findByText(/Halaman 1 dari 3 · 60 entri/)).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Berikutnya" }));

    await waitFor(() => {
      expect(panggilan.some((p) => p.url.includes("halaman=2"))).toBe(true);
    });
    expect(await screen.findByText(/Halaman 2 dari 3/)).toBeDefined();
  });

  it("Manager ditolak halaman Audit log (penjaga rute) dan API tidak dipanggil", async () => {
    const panggilan = stubFetch("manager");

    render(<RouterProvider router={buatRouter("/audit")} />);

    expect(await screen.findByText("Tidak berwenang")).toBeDefined();
    expect(panggilan.some((p) => p.url.includes("/admin/audit-logs"))).toBe(false);
  });
});
