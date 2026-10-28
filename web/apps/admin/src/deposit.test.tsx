import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

/**
 * Uji antrean top-up deposit admin (DEP-04/DEP-05): Manager hanya dapat
 * meninjau/menolak dengan alasan, sedangkan kredit saldo hanya dapat dibuat
 * Superadmin dengan nominal final dan Idempotency-Key.
 */

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

const topup = {
  id: "topup-1",
  branch_id: "branch-1",
  branch_nama: "Grahita Bandung",
  bank_account_id: "bank-1",
  bank: "BCA",
  display_nomor: "•••• 1234",
  status: "pending_review",
  declared_amount: 200000,
  proof_nama_file: "bukti-transfer.jpg",
  created_at: "2026-10-28T03:00:00Z",
};

function pasangFetch(peran: "manager" | "superadmin") {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      panggilan.push({ url, init });
      if (url.endsWith("/auth/me")) {
        return json({
          data: {
            user: {
              id: "admin-1",
              kind: "admin",
              role: peran,
              nama: peran === "manager" ? "Manager Uji" : "Superadmin Uji",
              email: `${peran}@example.test`,
            },
          },
        });
      }
      if (url.endsWith("/admin/deposits")) {
        return json({
          data: [
            {
              branch_id: "branch-1",
              branch_nama: "Grahita Bandung",
              is_cnc: true,
              available_balance: 300000,
              journal_version: 4,
              pending_topup_count: 1,
              pending_topup_amount_declared: 200000,
            },
          ],
        });
      }
      if (url.includes("/admin/deposit-topups?")) {
        return json({ data: [topup], meta: { total: 1 } });
      }
      if (url.endsWith(`/admin/deposit-topups/${topup.id}/decision`) && init?.method === "POST") {
        return json({ data: { id: topup.id } });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

function bukaDeposit() {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/deposit"] }),
  });
  render(<RouterProvider router={router} />);
}

function keputusan(panggilan: Panggilan[]) {
  return panggilan.filter(
    (p) => p.url.endsWith(`/admin/deposit-topups/${topup.id}/decision`) && p.init?.method === "POST",
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Admin Portal — keputusan top-up deposit", () => {
  it("Manager dapat Review dan Tolak hanya dengan alasan, tanpa akses Verifikasi", async () => {
    const panggilan = pasangFetch("manager");
    bukaDeposit();

    expect(await screen.findByRole("button", { name: "Review" }, { timeout: 5_000 })).toBeDefined();
    expect(screen.getByText("Kredit saldo hanya dapat diputuskan Superadmin.")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Verifikasi" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Review" }));
    let dialog = await screen.findByRole("dialog", { name: "Tandai perlu review" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan keputusan" }));
    expect(await within(dialog).findByText("Alasan wajib diisi.")).toBeDefined();
    expect(keputusan(panggilan)).toHaveLength(0);

    fireEvent.change(within(dialog).getByLabelText("Alasan"), {
      target: { value: "Nominal OCR tidak sama dengan bukti" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan keputusan" }));
    await waitFor(() => expect(keputusan(panggilan)).toHaveLength(1));
    expect(JSON.parse(String(keputusan(panggilan)[0]?.init?.body))).toEqual({
      action: "review",
      reason: "Nominal OCR tidak sama dengan bukti",
    });
    expect((keputusan(panggilan)[0]?.init?.headers as Record<string, string>)["Idempotency-Key"]).toBeUndefined();

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: "Tolak" }));
    dialog = await screen.findByRole("dialog", { name: "Tolak top-up" });
    fireEvent.change(within(dialog).getByLabelText("Alasan"), {
      target: { value: "Bukti transfer tidak dapat dibaca" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan keputusan" }));
    await waitFor(() => expect(keputusan(panggilan)).toHaveLength(2));
    expect(JSON.parse(String(keputusan(panggilan)[1]?.init?.body))).toEqual({
      action: "reject",
      reason: "Bukti transfer tidak dapat dibaca",
    });
  });

  it("Superadmin wajib memberi alasan saat nominal final berbeda dan mengirim key idempotensi", async () => {
    const panggilan = pasangFetch("superadmin");
    bukaDeposit();

    fireEvent.click(await screen.findByRole("button", { name: "Verifikasi" }, { timeout: 5_000 }));
    const dialog = await screen.findByRole("dialog", { name: "Verifikasi top-up" });
    fireEvent.change(within(dialog).getByLabelText("Nominal kredit final"), {
      target: { value: "180000" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan keputusan" }));
    expect(
      await within(dialog).findByText("Nominal berbeda dari yang diajukan cabang; alasan wajib diisi."),
    ).toBeDefined();
    expect(keputusan(panggilan)).toHaveLength(0);

    fireEvent.change(within(dialog).getByLabelText("Alasan (wajib bila nominal berbeda)"), {
      target: { value: "Nominal valid pada mutasi bank adalah Rp180.000" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan keputusan" }));
    await waitFor(() => expect(keputusan(panggilan)).toHaveLength(1));

    const kirim = keputusan(panggilan)[0];
    expect(JSON.parse(String(kirim?.init?.body))).toEqual({
      action: "verify",
      approved_amount: 180000,
      reason: "Nominal valid pada mutasi bank adalah Rp180.000",
    });
    expect((kirim?.init?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
  });
});
