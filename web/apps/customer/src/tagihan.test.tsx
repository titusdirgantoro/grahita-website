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

describe("Portal Cabang — ringkasan piutang Tempo (TMP-24)", () => {
  it("menampilkan ledger, bukti pending, dan tiga bucket umur dari API", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/auth/me")) return json({ data: { user: { id: "u1", kind: "branch", role: "branch", branch_id: "b1", nama: "Grahita Bandung", email: "bandung@example.test" } } });
      if (url.endsWith("/me/branch")) return json({ data: { id: "b1", nama: "Grahita Bandung", is_tempo: true, has_tempo_history: true } });
      if (url.endsWith("/tempo/overview")) return json({ data: {
        total_receivable: 550000, overdue: 550000, pending_proof: 20000, hold_active: true,
        age_0_30: 300000, age_31_60: 200000, age_over_60: 50000,
        items: [{ period_month: "2026-08", due_date: "2026-10-19", total: 300000, paid: 0, remaining: 300000, status: "lewat_jatuh_tempo" }],
      } });
      if (url.endsWith("/tempo/statements") || url.endsWith("/tempo/repayments") || url.endsWith("/bank-accounts")) return json({ data: [] });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }));

    render(<RouterProvider router={router()} />);

    expect(await screen.findByText(/Piutang:/, {}, { timeout: 15000 })).toBeDefined();
    expect(screen.getByText(/Piutang:/).textContent).toMatch(/Rp\s*550\.000.*Rp\s*550\.000/);
    expect(screen.getByText(/Bukti menunggu verifikasi/).textContent).toMatch(/Rp\s*20\.000/);
    const umur = screen.getByText(/Sejak jatuh tempo/).textContent ?? "";
    expect(umur).toMatch(/0–30 hari Rp\s*300\.000/);
    expect(umur).toMatch(/31–60 hari Rp\s*200\.000/);
    expect(umur).toMatch(/>60 hari Rp\s*50\.000/);
  });
});
