import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import type { PembayaranPesanan, RingkasanDeposit } from "./pages/deposit";
import { statusBayar } from "./pages/deposit";

/**
 * Uji deposit cabang (C4; D3 §7.1): top-up pending tidak menambah saldo,
 * "Gunakan deposit" mengirim versi yang dilihat layar + Idempotency-Key, dan
 * konflik 409 memuat ulang tanpa pengurangan diam-diam. Status pembayaran
 * revisi tidak pernah tampil LUNAS sebelum settlement Done.
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const penggunaCabang = {
  id: "u1",
  kind: "branch",
  role: "branch",
  nama: "Grahita Bandung",
  email: "bandung@example.test",
  branch_id: "b1",
};

const saldo: RingkasanDeposit = {
  branch_id: "b1",
  available_balance: 300000,
  journal_version: 4,
  pending_topup_count: 1,
  pending_topup_amount_declared: 150000,
};

function pembayaran(ubah: Partial<PembayaranPesanan> = {}): PembayaranPesanan {
  return {
    order_id: "o1",
    status: "submitted",
    versi: 2,
    current_total: 500000,
    committed_deposit: 100000,
    remaining_due: 400000,
    is_lunas: false,
    allocations: [],
    balance: saldo,
    ...ubah,
  };
}

interface Panggilan {
  url: string;
  init?: RequestInit;
}

function pasangFetch(alokasi: () => Response, bayar: () => PembayaranPesanan = () => pembayaran()) {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/deposit")) return json({ data: saldo });
      if (input.includes("/deposit/topups")) return json({ data: [], meta: { total: 0 } });
      if (input.includes("/deposit/transactions")) return json({ data: [], meta: { total: 0 } });
      if (input.endsWith("/bank-accounts")) return json({ data: [] });
      if (input.endsWith("/orders/o1/payment")) return json({ data: bayar() });
      if (input.endsWith("/orders/o1/deposit-allocations")) return alokasi();
      if (input.endsWith("/orders/o1/proofs")) return json({ data: [] });
      if (input.endsWith("/orders/o1")) {
        return json({
          data: { id: "o1", nomor: "2026-10-0001", status: "submitted", is_cnc: false, total: 500000, jumlah_item: 1, created_at: "2026-10-10T00:00:00Z", lines: [] },
        });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

function buka(path: string) {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Customer Portal — deposit (C4)", () => {
  it("menampilkan saldo tersedia terpisah dari top-up pending", async () => {
    pasangFetch(() => json({}));
    buka("/deposit");
    const nilai = await screen.findByText((_, el) => el?.getAttribute("data-uji") === "saldo-tersedia");
    expect(nilai.textContent).toContain("300.000");
    expect(nilai.textContent).not.toContain("450.000");
    expect(screen.getByText(/belum menambah saldo/i)).toBeTruthy();
  });

  it("Gunakan deposit mengirim versi layar dan key sama saat dicoba ulang setelah gagal jaringan", async () => {
    let percobaan = 0;
    const panggilan = pasangFetch(() => {
      percobaan += 1;
      if (percobaan === 1) throw new TypeError("jaringan putus");
      return json({ data: {} }, 201);
    });
    buka("/pesanan/o1");
    const input = await screen.findByLabelText(/Gunakan deposit/);
    fireEvent.change(input, { target: { value: "150000" } });
    fireEvent.click(screen.getByRole("button", { name: "Gunakan deposit" }));
    await screen.findByText(/Terjadi kesalahan/);
    fireEvent.click(screen.getByRole("button", { name: "Gunakan deposit" }));
    await screen.findByText(/dipakai untuk pesanan ini/);

    const post = panggilan.filter((p) => p.url.endsWith("/deposit-allocations") && p.init?.method === "POST");
    expect(post).toHaveLength(2);
    const body = JSON.parse(String(post[0]?.init?.body));
    expect(body).toEqual({ amount: 150000, expected_journal_version: 4, expected_order_version: 2 });
    const kunci = post.map((p) => (p.init?.headers as Record<string, string>)["Idempotency-Key"]);
    expect(kunci[0]).toBeTruthy();
    expect(kunci[1]).toBe(kunci[0]);
  });

  it("konflik versi memuat ulang rincian tanpa mengklaim berhasil", async () => {
    const panggilan = pasangFetch(() =>
      json({ error: { code: "DEPOSIT_VERSION_CONFLICT", message: "berubah" } }, 409),
    );
    buka("/pesanan/o1");
    const input = await screen.findByLabelText(/Gunakan deposit/);
    fireEvent.change(input, { target: { value: "50000" } });
    fireEvent.click(screen.getByRole("button", { name: "Gunakan deposit" }));
    await screen.findByText(/baru saja berubah/);
    await waitFor(() =>
      expect(panggilan.filter((p) => p.url.endsWith("/orders/o1/payment")).length).toBeGreaterThanOrEqual(2),
    );
    expect(screen.queryByText(/dipakai untuk pesanan ini/)).toBeNull();
  });

  it("revisi pending tidak pernah berlabel Lunas", () => {
    expect(statusBayar(pembayaran({ revision_payment_state: "revision_underpayment" })).label).not.toBe("Lunas");
    expect(statusBayar(pembayaran({ revision_payment_state: "revision_overpayment_pending", remaining_due: 0 })).label).not.toBe("Lunas");
    expect(statusBayar(pembayaran({ remaining_due: 0 })).label).not.toBe("Lunas");
    expect(statusBayar(pembayaran({ status: "done", is_lunas: true, remaining_due: 0 })).label).toBe("Lunas");
  });
});
