import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function router() {
  return createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/tagihan"] }) });
}

describe("Admin Portal — ringkasan piutang Tempo (TMP-24)", () => {
  it("menampilkan ledger, bukti pending, dan tiga bucket umur dari API", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/auth/me")) return json({ data: { user: { id: "a1", kind: "admin", role: "superadmin", nama: "Rina", email: "rina@example.test" } } });
      if (url.endsWith("/admin/tempo/overview")) return json({ data: {
        total_receivable: 550000, overdue: 550000, pending_proof: 20000,
        age_0_30: 300000, age_31_60: 200000, age_over_60: 50000,
        items: [{ branch_id: "b1", branch_name: "Grahita Bandung", period_month: "2026-08", due_date: "2026-10-19", total: 300000, paid: 0, remaining: 300000, status: "lewat_jatuh_tempo", age_days: 10 }],
      } });
      if (url.endsWith("/admin/tempo/repayments") || url.endsWith("/admin/tempo/statements")) return json({ data: [] });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }));

    render(<RouterProvider router={router()} />);

    expect(await screen.findByText(/Total piutang/, {}, { timeout: 15000 })).toBeDefined();
    expect(screen.getByText(/Total piutang/).textContent).toMatch(/Rp\s*550\.000.*Rp\s*550\.000/);
    expect(screen.getByText(/Bukti menunggu verifikasi/).textContent).toMatch(/Rp\s*20\.000/);
    const umur = screen.getByText(/Umur piutang sejak jatuh tempo/).textContent ?? "";
    expect(umur).toMatch(/0–30 hari Rp\s*300\.000/);
    expect(umur).toMatch(/31–60 hari Rp\s*200\.000/);
    expect(umur).toMatch(/>60 hari Rp\s*50\.000/);
  });
});
