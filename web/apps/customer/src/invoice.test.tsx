import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import type { Invoice } from "./pages/invoice";

/**
 * Uji pInvoice (kartu F1-19): riwayat invoice cabang sesi, versi lama yang
 * tetap tampil setelah revisi, unduhan lewat presigned URL, dan invoice yang
 * belum selesai yang tidak bisa diunduh.
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const penggunaCabang = {
  id: "u1",
  kind: "branch",
  role: "branch",
  nama: "Grahita Bandung",
  email: "bandung@example.test",
  branch_id: "b1",
};

interface Panggilan {
  url: string;
  init?: RequestInit;
}

function fetchInvoice(invoices: Invoice[], tautan?: { url: string; nama_file: string }) {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.includes("/invoices")) return json({ data: invoices });
      if (input.includes("/documents/")) {
        return json({
          data: {
            url: tautan?.url ?? "/files/documents/rahasia/invoice_pdf-0.pdf?download=Invoice.pdf",
            expires_in: 300,
            nama_file: tautan?.nama_file ?? "Invoice-2026-10-0001.pdf",
            mime_type: "application/pdf",
          },
        });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

function buatRouter(path: string) {
  return createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
}

function invoice(ubah: Partial<Invoice> & { order_id: string; nomor: string }): Invoice {
  return {
    status: "done",
    versi: 0,
    is_cnc: false,
    total: 250000,
    jumlah_item: 3,
    versi_dokumen: 0,
    ukuran_byte: 1024,
    terbaru: true,
    dapat_diunduh: true,
    ...ubah,
  } as Invoice;
}

const RIAWAYAT: Invoice[] = [
  invoice({ order_id: "o1", nomor: "2026-10-0001", tanggal: "2026-10-08", dokumen_id: "d-1" }),
  invoice({
    order_id: "o2",
    nomor: "2026-10-0002",
    tanggal: "2026-10-09",
    status: "submitted",
    is_cnc: true,
    total: 150000,
    jumlah_item: 1,
    dokumen_id: undefined,
    versi_dokumen: null,
    ukuran_byte: 0,
    terbaru: false,
    dapat_diunduh: false,
  }),
  invoice({
    order_id: "o3",
    nomor: "2026-10-0003",
    tanggal: "2026-10-05",
    versi: 1,
    total: 260000,
    jumlah_item: 2,
    dokumen_id: "d-3b",
    versi_dokumen: 1,
  }),
  invoice({
    order_id: "o3",
    nomor: "2026-10-0003",
    tanggal: "2026-10-05",
    versi: 1,
    total: 250000,
    jumlah_item: 2,
    dokumen_id: "d-3a",
    versi_dokumen: 0,
    terbaru: false,
  }),
];

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Customer Portal — Riwayat invoice (F1-19)", () => {
  it("menampilkan nomor, versi (termasuk versi lama), dan status dari API cabang sesi", async () => {
    const panggilan = fetchInvoice(RIAWAYAT);

    render(<RouterProvider router={buatRouter("/invoice")} />);

    // Baris muncul: satu dokumen per versi, jadi invoice yang sudah direvisi
    // tampil dua kali (Revisi 1 + versi lama).
    expect(await screen.findByText("2026-10-0001")).toBeDefined();
    expect(screen.getAllByText("2026-10-0003")).toHaveLength(2);
    expect(await screen.findByText("Revisi 1")).toBeDefined();
    expect(screen.getByText(/versi lama/)).toBeDefined();
    // Pesanan yang masih menunggu verifikasi tampil tanpa PDF.
    const tabel = screen.getByRole("table", { name: "Riwayat invoice cabang" });
    expect(within(tabel).getAllByRole("row")).toHaveLength(5); // header + 4 baris
    expect(screen.getByText("Menunggu verifikasi")).toBeDefined();

    // Tiga dokumen siap diunduh; yang belum selesai memakai tombol nonaktif.
    const unduh = screen.getAllByRole("button", { name: "Unduh" });
    expect(unduh).toHaveLength(3);
    const belum = screen.getByRole("button", { name: "Belum tersedia" });
    expect((belum as HTMLButtonElement).disabled).toBe(true);

    // Sumber data adalah endpoint cabang sesi: tanpa parameter cabang apa pun.
    const daftar = panggilan.filter((p) => p.url.includes("/invoices"));
    expect(daftar).toHaveLength(1);
    expect(daftar[0].url).toBe("/api/v1/invoices");
    expect((daftar[0].init?.method ?? "GET")).toBe("GET");
  });

  it("mengunduh lewat presigned URL berumur pendek, bukan tautan berkas langsung", async () => {
    const panggilan = fetchInvoice(RIAWAYAT, {
      url: "/files/documents/o1/invoice_pdf-0.pdf?download=Invoice-2026-10-0001.pdf",
      nama_file: "Invoice-2026-10-0001.pdf",
    });
    const buka = vi.spyOn(window, "open").mockImplementation(() => null);

    render(<RouterProvider router={buatRouter("/invoice")} />);
    const unduh = await screen.findAllByRole("button", { name: "Unduh" });

    fireEvent.click(unduh[0]);

    await waitFor(() => {
      expect(panggilan.some((p) => p.url === "/api/v1/documents/d-1/download")).toBe(true);
    });
    await waitFor(() => {
      expect(buka).toHaveBeenCalledWith(
        "/files/documents/o1/invoice_pdf-0.pdf?download=Invoice-2026-10-0001.pdf",
        "_blank",
        "noopener,noreferrer",
      );
    });
    // UI tidak pernah menyusun URL berkas object storage sendiri.
    expect(panggilan.every((p) => !p.url.startsWith("/files/"))).toBe(true);
  });

  it("memakai filter periode dari URL dan mengirimkannya kembali ke API", async () => {
    const panggilan = fetchInvoice(RIAWAYAT);

    render(<RouterProvider router={buatRouter("/invoice?dari=2026-10-01&sampai=2026-10-31")} />);

    await waitFor(() => {
      expect(
        panggilan.some((p) => p.url === "/api/v1/invoices?dari=2026-10-01&sampai=2026-10-31"),
      ).toBe(true);
    });

    // Ubah periode lalu Terapkan: permintaan berikutnya memakai rentang baru.
    fireEvent.change(screen.getByLabelText("Dari tanggal"), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Terapkan" }));

    await waitFor(() => {
      expect(
        panggilan.some((p) => p.url === "/api/v1/invoices?dari=2026-09-01&sampai=2026-10-31"),
      ).toBe(true);
    });
  });

  it("menampilkan galat periode dari server sebagai pesan validasi", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
        return json(
          {
            error: {
              code: "VALIDATION_FAILED",
              message: "Filter periode tidak valid.",
              fields: { periode: "dari harus berformat YYYY-MM-DD" },
            },
          },
          400,
        );
      }),
    );

    render(<RouterProvider router={buatRouter("/invoice?dari=salah")} />);

    expect(await screen.findByText("Filter periode tidak valid.")).toBeDefined();
  });
});
