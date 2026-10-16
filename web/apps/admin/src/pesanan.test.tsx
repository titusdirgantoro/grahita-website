import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import { bolehRevisi, labelStatusRekening, uraiBilanganBulat, type FilterPesanan, type PesananDetail, type PesananHalaman } from "./pages/pesanan";

const DETAIL_ID = "11111111-1111-4111-8111-111111111111";

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

/** halamanUji menyusun satu halaman riwayat dengan jumlah total yang bisa diatur. */
function halamanUji(total: number): PesananHalaman {
  return {
    items: [
      {
        id: DETAIL_ID,
        nomor: "2026-10-01",
        versi: 0,
        branch_id: "b1",
        branch_nama: "Grahita Bandung",
        tanggal: "2026-10-01",
        status: "submitted",
        verif_status: "belum",
        is_cnc: false,
        is_sertifikat: false,
        total: 200000,
        jumlah_item: 2,
        terlambat: false,
        email_gagal: false,
        created_at: "2026-10-01T03:00:00Z",
        updated_at: "2026-10-01T03:00:00Z",
      },
    ],
    total,
    halaman: 1,
    per_halaman: 25,
  };
}

const DETAIL_UJI: PesananDetail = {
  id: DETAIL_ID,
  nomor: "2026-10-01",
  versi: 0,
  branch_id: "b1",
  branch_nama: "Grahita Bandung",
  branch_kepala: "Sri Wahyuni",
  branch_email: "bandung@example.test",
  branch_cnc: true,
  status: "submitted",
  verif_status: "perlu_review",
  alasan_review: "kurang_bayar",
  is_cnc: true,
  is_sertifikat: false,
  tanggal: "2026-10-01",
  jatuh_tempo: "2026-10-08",
  created_at: "2026-10-01T03:00:00Z",
  updated_at: "2026-10-01T03:00:00Z",
  catatan: "Mohon dikirim pagi",
  note_admin: "Sudah dihubungi cabang",
  total: 200000,
  jumlah_item: 2,
  terlambat: true,
  email_gagal: true,
  lines: [
    {
      line_no: 1,
      jenis: "product",
      nama: "Bio Magnetik",
      qty: 2,
      harga_satuan: 100000,
      subtotal: 200000,
      serial_number: "20261001001",
    },
  ],
  timeline: [{ at: "2026-10-01T03:00:00Z", teks: "Pesanan dikirim oleh Grahita Bandung", actor_kind: "branch" }],
  dokumen: [{ id: "d1", jenis: "invoice_pdf", versi: 0, ukuran_byte: 1234, created_at: "2026-10-01T04:00:00Z" }],
  verifikasi: [],
};

/** Pesanan Done (punya nomor + invoice) — prasyarat revisi invoice (AP-REV-01). */
const DETAIL_DONE_UJI: PesananDetail = {
  ...DETAIL_UJI,
  status: "done",
  verif_status: "cocok",
  alasan_review: undefined,
  versi: 0,
  done_at: "2026-10-01T05:00:00Z",
  verifikasi: [
    {
      id: "v1",
      metode: "manual",
      total_pesanan: 200000,
      total_terbaca: 200000,
      selisih: 0,
      hasil: "cocok",
      admin_nama: "Budi",
      created_at: "2026-10-01T05:00:00Z",
    },
  ],
};

/** Hasil POST /revise (ReviseResult) untuk jawaban 200 pada uji. */
const REVISE_HASIL_UJI = {
  order_id: DETAIL_ID,
  nomor: "2026-10-01",
  versi: 1,
  total: 600000,
  jumlah_item: 5,
  delta_total: 400000,
  lines: [],
  revisi: {
    id: "rv1",
    versi_lama: 0,
    versi_baru: 1,
    alasan: "salah harga",
    delta_total: 400000,
    admin_nama: "Budi",
    created_at: "2026-10-01T06:00:00Z",
  },
};

/**
 * MATRIKS_UJI adalah jawaban GET /admin/prices (PriceMatrix) untuk pemilihan
 * produk pada baris baru: hanya produk yang punya harga di cabang itu (sel).
 */
const MATRIKS_UJI = {
  produk: [
    {
      id: "pr-1",
      kode: "BM",
      nama: "Bio Magnetik",
      jenis: "reguler",
      harga_default: 100000,
      urutan: 1,
      is_active: true,
    },
    {
      id: "pr-2",
      kode: "KK",
      nama: "Kalung Kesehatan",
      jenis: "reguler",
      harga_default: 150000,
      urutan: 2,
      is_active: true,
    },
  ],
  cabang: [{ id: "b1", nama: "Grahita Bandung", is_active: true }],
  sel: [
    { branch_id: "b1", product_id: "pr-1", harga: 100000 },
    { branch_id: "b1", product_id: "pr-2", harga: 150000 },
  ],
};

/** Jawaban semu satu panggilan POST /revise. */
interface JawabanRevisi {
  status: number;
  code?: string;
  message?: string;
  fields?: Record<string, string>;
}

interface StubUji {
  panggilan: string[];
  posts: { url: string; body: unknown; headers: Record<string, string> }[];
}

/** stubPesanan memasang fetch semu untuk seluruh endpoint F1-15/F1-18. */
function stubPesanan(opsi?: {
  total?: number;
  detail?: PesananDetail;
  grup?: FilterPesanan["grup"];
  /** Galat yang dikembalikan POST /verify (mis. 409 ORDER_ALREADY_DONE). */
  verifyGagal?: { status: number; code: string; message: string };
  /** Ditunggu sebelum POST /verify menjawab — untuk menguji keadaan sibuk. */
  verifyTertahan?: Promise<unknown>;
  aiAktif?: boolean;
  proofs?: Array<Record<string, unknown>>;
  /** Jawaban berurutan POST /revise (F2-25); entri terakhir dipakai berulang. */
  revise?: JawabanRevisi[];
  /** Ditunggu sebelum POST /revise menjawab — untuk menguji klik ganda. */
  reviseTertahan?: Promise<unknown>;
}): StubUji {
  const panggilan: string[] = [];
  const posts: { url: string; body: unknown; headers: Record<string, string> }[] = [];
  const total = opsi?.total ?? 1;
  let revisiKe = 0;

  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      panggilan.push(url);

      if (url.endsWith("/auth/me")) {
        return json({
          data: { user: { id: "a1", kind: "admin", role: "manager", nama: "Budi", email: "budi@example.test" } },
        });
      }
      if (init?.method === "POST") {
        posts.push({
          url,
          body: init.body ? JSON.parse(String(init.body)) : null,
          headers: (init.headers ?? {}) as Record<string, string>,
        });
        if (url.includes("/notes")) return json({ data: opsi?.detail ?? DETAIL_UJI });
        if (url.includes("/resend")) return json({ data: { id: "e2", jenis: "invoice_cabang", status: "queued" } });
        if (url.endsWith(`/admin/orders/${DETAIL_ID}/verify`)) {
          if (opsi?.verifyTertahan) await opsi.verifyTertahan;
          if (opsi?.verifyGagal) {
            return json({ error: { code: opsi.verifyGagal.code, message: opsi.verifyGagal.message } }, opsi.verifyGagal.status);
          }
          return json({
            data: {
              order: {
                id: DETAIL_ID,
                nomor: "2026-10-01",
                versi: 0,
                branch_id: "b1",
                status: "done",
                verif_status: "cocok",
                total: 200000,
              },
              verifikasi: {
                id: "v1",
                order_id: DETAIL_ID,
                metode: "manual",
                total_pesanan: 200000,
                total_terbaca: 200000,
                selisih: 0,
                hasil: "cocok",
                admin_nama: "Budi",
                created_at: "2026-10-01T05:00:00Z",
              },
            },
          });
        }
        if (url.endsWith(`/admin/orders/${DETAIL_ID}/revise`)) {
          const daftar = opsi?.revise ?? [{ status: 200 }];
          const jawab = daftar[Math.min(revisiKe, daftar.length - 1)];
          revisiKe += 1;
          if (opsi?.reviseTertahan) await opsi.reviseTertahan;
          if (jawab.status >= 400) {
            return json(
              {
                error: {
                  code: jawab.code ?? "UNKNOWN",
                  message: jawab.message ?? "gagal",
                  ...(jawab.fields ? { fields: jawab.fields } : {}),
                },
              },
              jawab.status,
            );
          }
          return json({ data: REVISE_HASIL_UJI });
        }
        return json({ data: {} }, 201);
      }
      if (url.includes("/admin/branches")) {
        return json({ data: [{ id: "b1", nama: "Grahita Bandung", is_active: true }] });
      }
      if (url.includes("/admin/prices")) {
        return json({ data: MATRIKS_UJI });
      }
      if (url.includes("/download")) {
        return json({
          data: { url: "https://files.test/bukti.png", expires_in: 300, nama_file: "bukti.png", mime_type: "image/png" },
        });
      }
      if (url.endsWith(`/admin/orders/${DETAIL_ID}/bank-accounts`)) {
        return json({
          data: {
            allowlist_version: 3,
            ai_active: opsi?.aiAktif ?? true,
            accounts: [
              { id: "ba-1", bank: "BCA", nama_pemilik: "PT Grahita", display_nomor: "••••1234" },
            ],
          },
        });
      }
      if (url.includes(`/admin/orders/${DETAIL_ID}/proofs`)) {
        return json({
          data: opsi?.proofs ?? [
            {
              id: "p1",
              order_id: DETAIL_ID,
              nama_file: "bukti.png",
              mime_type: "image/png",
              ukuran_byte: 1000,
              uploaded_by_kind: "branch",
              created_at: "2026-10-01T03:30:00Z",
            },
          ],
        });
      }
      if (url.includes(`/admin/orders/${DETAIL_ID}/emails`)) {
        return json({
          data: [
            {
              id: "e1",
              jenis: "invoice_cabang",
              to_addr: ["bandung@example.test"],
              subject: "Invoice 2026-10-01",
              status: "failed",
              attempts: 6,
              last_error: "smtp menolak",
              created_at: "2026-10-01T04:00:00Z",
            },
          ],
        });
      }
      if (url.includes(`/admin/orders/${DETAIL_ID}`)) {
        return json({ data: opsi?.detail ?? DETAIL_UJI });
      }
      if (url.includes("/admin/orders")) {
        const halaman = halamanUji(total);
        if (opsi?.grup) {
          return json({
            data: {
              ...halaman,
              grup: opsi.grup,
              ringkasan: [{ kunci: "2026-10", label: "", jumlah: 3, total: 600000 }],
            },
          });
        }
        return json({ data: halaman });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return { panggilan, posts };
}

describe("Riwayat pemesanan admin (F1-15)", () => {
  it("menampilkan daftar dan meminta halaman dengan paginasi server", async () => {
    const { panggilan } = stubPesanan({ total: 1 });
    render(<RouterProvider router={buatRouter("/pesanan")} />);

    expect(await screen.findByText("2026-10-01")).toBeDefined();
    expect(within(screen.getByRole("table")).getByText("Grahita Bandung")).toBeDefined();
    expect(within(screen.getByRole("table")).getByText("Submitted")).toBeDefined();
    expect(screen.getByText("1 pesanan · halaman 1 dari 1")).toBeDefined();

    const daftar = panggilan.find((u) => u.includes("/admin/orders?"));
    expect(daftar).toBeDefined();
    expect(daftar).toContain("per_halaman=25");
    expect(daftar).toContain("halaman=1");
  });

  it("filter C&C mengirim cnc=true ke API", async () => {
    const { panggilan } = stubPesanan({ total: 1 });
    render(<RouterProvider router={buatRouter("/pesanan")} />);
    await screen.findByText("2026-10-01");

    fireEvent.click(screen.getByRole("button", { name: "C&C" }));

    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("cnc=true"))).toBe(true);
    });
  });

  it("pencarian mengirim kata kunci ke API", async () => {
    const { panggilan } = stubPesanan({ total: 1 });
    render(<RouterProvider router={buatRouter("/pesanan")} />);
    await screen.findByText("2026-10-01");

    fireEvent.change(screen.getByLabelText(/Cari nomor/), { target: { value: "bio" } });
    fireEvent.click(screen.getByRole("button", { name: "Cari" }));

    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("cari=bio"))).toBe(true);
    });
  });

  it("paginasi 'Berikutnya' meminta halaman 2", async () => {
    const { panggilan } = stubPesanan({ total: 30 });
    render(<RouterProvider router={buatRouter("/pesanan")} />);
    await screen.findByText("2026-10-01");

    fireEvent.click(screen.getByRole("button", { name: "Berikutnya" }));

    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("halaman=2"))).toBe(true);
    });
  });

  it("grouping per bulan menampilkan ringkasan grup", async () => {
    stubPesanan({ total: 3, grup: "bulan" });
    render(<RouterProvider router={buatRouter("/pesanan?grup=bulan")} />);

    expect(await screen.findByText("Ringkasan grup")).toBeDefined();
    expect(await screen.findByText("2026-10")).toBeDefined();
    expect(screen.getAllByText(/3 pesanan/).length).toBeGreaterThanOrEqual(1);
  });

  it("detail menampilkan header, baris + serial, timeline, catatan internal, bukti, dan email", async () => {
    stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);

    expect(await screen.findByText("Bio Magnetik")).toBeDefined();
    expect(screen.getByText("2026-10-01")).toBeDefined();
    expect(screen.getByText("20261001001")).toBeDefined();
    expect(screen.getByText(/Pesanan dikirim oleh Grahita Bandung/)).toBeDefined();
    expect(screen.getByDisplayValue("Sudah dihubungi cabang")).toBeDefined();
    expect(screen.getByText("Mohon dikirim pagi")).toBeDefined();
    expect(screen.getByText("bukti.png")).toBeDefined();
    expect(await screen.findByText("Invoice ke cabang")).toBeDefined();
    expect(screen.getByText("smtp menolak")).toBeDefined();
    expect(screen.getByText("Invoice")).toBeDefined();
  });

  it("menyimpan catatan internal lewat POST /notes", async () => {
    const { posts, panggilan } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    const sebelum = panggilan.length;
    const catatan = screen.getByLabelText("Note admin");
    fireEvent.change(catatan, { target: { value: "tindak lanjut pusat" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan catatan" }));

    await waitFor(() => {
      expect(posts.some((p) => p.url.includes("/notes"))).toBe(true);
    });
    const kirim = posts.find((p) => p.url.includes("/notes"));
    expect(kirim?.url).toContain(`/api/v1/admin/orders/${DETAIL_ID}/notes`);
    expect(kirim?.body).toEqual({ catatan: "tindak lanjut pusat" });
    // Detail dimuat ulang setelah catatan tersimpan.
    await waitFor(() => {
      expect(panggilan.length).toBeGreaterThan(sebelum + 1);
    });
  });

  it("tombol zoom bukti mengambil tautan unduh berumur pendek", async () => {
    const { panggilan } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("bukti.png");

    fireEvent.click(screen.getByRole("button", { name: "Zoom" }));

    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/download"))).toBe(true);
    });
    expect(await screen.findByAltText("Pratinjau bukti.png")).toBeDefined();
  });

  it("kirim ulang email gagal memanggil endpoint resend", async () => {
    const { posts } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Invoice ke cabang");

    fireEvent.click(screen.getByRole("button", { name: "Kirim ulang" }));

    await waitFor(() => {
      expect(posts.some((p) => p.url.includes("/resend"))).toBe(true);
    });
    expect(posts.find((p) => p.url.includes("/resend"))?.url).toContain(
      `/api/v1/admin/orders/${DETAIL_ID}/emails/invoice_cabang/resend`,
    );
  });

  it("unduh dokumen memakai tautan presigned domain dokumen (F1-17)", async () => {
    const buka = vi.fn();
    vi.stubGlobal("open", buka);
    const { panggilan } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Invoice");

    fireEvent.click(screen.getByRole("button", { name: "Unduh" }));

    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/documents/d1/download"))).toBe(true);
    });
    expect(buka).toHaveBeenCalled();
    // Kunci storage tidak pernah dipakai UI: tautan datang dari endpoint download.
    expect(panggilan.some((u) => u.includes("storage_key"))).toBe(false);
  });

  it("tombol buat dokumen hanya untuk pesanan Done dan idempoten lewat endpoint admin", async () => {
    const { posts, panggilan } = stubPesanan({ detail: { ...DETAIL_UJI, status: "done" } });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);

    const tombol = await screen.findByRole("button", { name: "Buat dokumen" });
    fireEvent.click(tombol);

    await waitFor(() => {
      expect(posts.some((p) => p.url.endsWith(`/admin/orders/${DETAIL_ID}/documents`))).toBe(true);
    });
    const kirim = posts.find((p) => p.url.endsWith(`/admin/orders/${DETAIL_ID}/documents`));
    expect(kirim?.url).toBe(`/api/v1/admin/orders/${DETAIL_ID}/documents`);
    // Daftar dokumen dimuat ulang setelah render.
    await waitFor(() => {
      expect(panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length).toBeGreaterThan(1);
    });
  });
});

describe("Verifikasi pembayaran manual admin (F1-18, PRD §4.2 AP-ORD-07)", () => {
  const URL_VERIFY = `/api/v1/admin/orders/${DETAIL_ID}/verify`;

  it("menerjemahkan seluruh status allowlist ke label operator", () => {
    expect(["allowed", "not_allowed", "inactive", "ambiguous", "unreadable", undefined].map(labelStatusRekening)).toEqual([
      "Sesuai", "Tidak diizinkan", "Nonaktif", "Ambigu", "Tidak terbaca", "Tidak terbaca",
    ]);
  });

  it("nilai ulang hanya aktif ketika AI hidup dan seluruh bukti sudah mempunyai OCR", async () => {
    const proofOCR = [{ id: "p1", order_id: DETAIL_ID, nama_file: "bukti.png", mime_type: "image/png", ukuran_byte: 1000, uploaded_by_kind: "branch", created_at: "2026-10-01T03:30:00Z", ocr_at: "2026-10-01T03:31:00Z", ocr_bank: "BCA", ocr_rek_akhir: "1234", ocr_confidence: 0.98 }];
    stubPesanan({ aiAktif: true, proofs: proofOCR });
    const tampilan = render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    const tombolAktif = await screen.findByRole("button", { name: "Nilai ulang rekening" });
    await waitFor(() => expect((tombolAktif as HTMLButtonElement).disabled).toBe(false));
    tampilan.unmount();

    stubPesanan({ aiAktif: false, proofs: proofOCR });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    expect((await screen.findByRole("button", { name: "Nilai ulang rekening" }) as HTMLButtonElement).disabled).toBe(true);
    expect(await screen.findByText(/AI\/OCR sedang dinonaktifkan/)).toBeDefined();
  });

  it("nilai ulang tetap nonaktif bila hasil OCR belum tersedia", async () => {
    stubPesanan({ aiAktif: true });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    expect((await screen.findByRole("button", { name: "Nilai ulang rekening" }) as HTMLButtonElement).disabled).toBe(true);
    expect(await screen.findByText(/setelah seluruh bukti mempunyai hasil OCR tersimpan/)).toBeDefined();
  });

  it("menyetujui lewat modal: total, nominal terbaca + selisih, lalu POST /verify dan menyegarkan detail", async () => {
    const { posts, panggilan } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(screen.getByRole("button", { name: "Verifikasi" }));
    const dialog = await screen.findByRole("dialog");

    // Total pesanan tampil di modal; selisih kosong sebelum nominal diisi.
    // NBSP dari Intl dinormalkan agar perbandingan teks stabil.
    const teksSelisih = () =>
      (dialog.querySelector('[data-uji="selisih-verifikasi"]')?.textContent ?? "").replace(/\u00a0/g, " ");
    expect(within(dialog).getByText("Rp 200.000")).toBeDefined();
    expect(teksSelisih()).toContain("—");

    // Nominal boleh memakai pemisah ribuan; yang dikirim tetap angka.
    fireEvent.change(screen.getByLabelText("Nominal di bukti transfer"), { target: { value: "200.000" } });
    expect(teksSelisih()).toContain("Rp 0");

    const sebelum = panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length;
    fireEvent.click(screen.getByRole("button", { name: "Setujui & selesaikan" }));

    await waitFor(() => {
      expect(posts.some((p) => p.url === URL_VERIFY)).toBe(true);
    });
    const kirim = posts.find((p) => p.url === URL_VERIFY);
    expect(kirim?.body).toEqual({
      hasil: "cocok",
      total_terbaca: 200000,
      proof_destinations: [{ payment_proof_id: "p1", bank_account_id: "ba-1" }],
    });

    // Modal tertutup dan detail disegarkan supaya log verifikasi + dokumen baru terlihat.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => {
      expect(panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length).toBeGreaterThan(sebelum);
    });
  });

  it("menandai perlu review mengirim alasan tertutup + catatan (pesanan tidak diselesaikan)", async () => {
    const { posts } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(screen.getByRole("button", { name: "Verifikasi" }));
    await screen.findByRole("dialog");

    fireEvent.change(screen.getByLabelText("Keputusan"), { target: { value: "perlu_review" } });

    const alasan = await screen.findByLabelText("Alasan review");
    expect(Array.from(alasan.querySelectorAll("option")).map((o) => (o as HTMLOptionElement).value)).toEqual([
      "kurang_bayar",
      "lebih_bayar",
      "tidak_terbaca",
      "rekening_beda",
      "lainnya",
    ]);
    fireEvent.change(alasan, { target: { value: "rekening_beda" } });
    fireEvent.change(screen.getByLabelText("Catatan verifikasi"), {
      target: { value: "transfer dari rekening lain" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Tandai perlu review" }));

    await waitFor(() => {
      expect(posts.some((p) => p.url === URL_VERIFY)).toBe(true);
    });
    expect(posts.find((p) => p.url === URL_VERIFY)?.body).toEqual({
      hasil: "perlu_review",
      alasan: "rekening_beda",
      catatan: "transfer dari rekening lain",
      proof_destinations: [{ payment_proof_id: "p1", bank_account_id: "ba-1" }],
    });
  });

  it("tanpa nominal terbaca hanya mengirim hasil (bukti non-OCR / tunai C&C)", async () => {
    const { posts } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(screen.getByRole("button", { name: "Verifikasi" }));
    await screen.findByRole("dialog");
    const tombolSetujui = screen.getByRole("button", { name: "Setujui & selesaikan" });
    await waitFor(() => expect((tombolSetujui as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(tombolSetujui);

    await waitFor(() => {
      expect(posts.some((p) => p.url === URL_VERIFY)).toBe(true);
    });
    expect(posts.find((p) => p.url === URL_VERIFY)?.body).toEqual({
      hasil: "cocok",
      proof_destinations: [{ payment_proof_id: "p1", bank_account_id: "ba-1" }],
    });
  });

  it("tombol Verifikasi hanya muncul untuk pesanan Submitted", async () => {
    stubPesanan({ detail: { ...DETAIL_UJI, status: "done" } });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    expect(screen.queryByRole("button", { name: "Verifikasi" })).toBeNull();
  });

  it("409 ORDER_ALREADY_DONE dijelaskan dan detail disegarkan (klik/verifikasi ganda)", async () => {
    const { panggilan } = stubPesanan({
      verifyGagal: { status: 409, code: "ORDER_ALREADY_DONE", message: "Pesanan ini sudah selesai diverifikasi." },
    });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(screen.getByRole("button", { name: "Verifikasi" }));
    await screen.findByRole("dialog");
    const sebelum = panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length;
    fireEvent.click(screen.getByRole("button", { name: "Setujui & selesaikan" }));

    expect(await screen.findByText(/sudah selesai diverifikasi\. Tampilan dimuat ulang\./)).toBeDefined();
    await waitFor(() => {
      expect(panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length).toBeGreaterThan(sebelum);
    });
  });

  it("mencegah klik ganda: tombol terkunci selama permintaan berjalan", async () => {
    let lepaskan: () => void = () => undefined;
    const tertahan = new Promise<void>((resolve) => {
      lepaskan = resolve;
    });
    const { posts } = stubPesanan({ verifyTertahan: tertahan });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(screen.getByRole("button", { name: "Verifikasi" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Setujui & selesaikan" }));

    const menunggu = await screen.findByRole("button", { name: "Mengirim…" });
    expect(menunggu.hasAttribute("disabled")).toBe(true);
    fireEvent.click(menunggu);
    expect(posts.filter((p) => p.url === URL_VERIFY).length).toBe(1);

    lepaskan();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});

describe("Revisi invoice pesanan Done (F2-25, PRD §4.3 AP-REV-01..05)", () => {
  const URL_REVISE = `/api/v1/admin/orders/${DETAIL_ID}/revise`;

  beforeEach(() => {
    document.cookie = "grahita_csrf=uji-csrf; path=/";
  });
  afterEach(() => {
    document.cookie = "grahita_csrf=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT";
  });

  /** bukaRevisi merender detail Done dan membuka modal revisi. */
  async function bukaRevisi() {
    const dialog = await screen.findByRole("dialog");
    return dialog;
  }

  it("hanya peran berwenang yang punya aksi (cabang tidak; server tetap otoritas izin)", () => {
    expect(bolehRevisi("superadmin")).toBe(true);
    expect(bolehRevisi("manager")).toBe(true);
    expect(bolehRevisi("branch")).toBe(false);
  });

  it("uraiBilanganBulat hanya menerima bilangan bulat non-negatif (menolak -5/1.5, bukan mengubahnya)", () => {
    expect(uraiBilanganBulat("5")).toBe(5);
    expect(uraiBilanganBulat("0")).toBe(0);
    expect(uraiBilanganBulat(" 12 ")).toBe(12);
    expect(uraiBilanganBulat("-5")).toBeNull();
    expect(uraiBilanganBulat("1.5")).toBeNull();
    expect(uraiBilanganBulat("1,5")).toBeNull();
    expect(uraiBilanganBulat("1e3")).toBeNull();
    expect(uraiBilanganBulat("")).toBeNull();
    expect(uraiBilanganBulat("abc")).toBeNull();
  });

  it("tombol revisi hanya muncul untuk pesanan Done", async () => {
    stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    expect(screen.queryByRole("button", { name: "Revisi invoice" })).toBeNull();
  });

  it("mengubah qty/harga + alasan: POST sekali dengan CSRF lalu menyegarkan detail & email", async () => {
    const { posts, panggilan } = stubPesanan({ detail: DETAIL_DONE_UJI });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();

    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "salah harga jual" } });
    fireEvent.change(within(dialog).getByLabelText("Qty Bio Magnetik"), { target: { value: "5" } });
    fireEvent.change(within(dialog).getByLabelText("Harga satuan Bio Magnetik"), { target: { value: "120000" } });

    const sebelum = panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length;
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    await waitFor(() => expect(posts.some((p) => p.url === URL_REVISE)).toBe(true));
    const kirim = posts.find((p) => p.url === URL_REVISE);
    expect(kirim?.body).toEqual({
      alasan: "salah harga jual",
      lines: [{ line_no: 1, qty: 5, harga_satuan: 120000 }],
    });
    expect(kirim?.headers["X-CSRF-Token"]).toBe("uji-csrf");
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1);

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => {
      expect(panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length).toBeGreaterThan(sebelum);
    });
  });

  it("mengubah komposisi baris: hapus baris lama + tambah produk baru (tanpa line_no)", async () => {
    const { posts } = stubPesanan({ detail: DETAIL_DONE_UJI });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();

    const pilih = await within(dialog).findByLabelText("Tambah produk");
    await waitFor(() => expect(within(pilih).getByRole("option", { name: /Kalung Kesehatan/ })).toBeDefined());
    fireEvent.change(pilih, { target: { value: "pr-2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Tambah baris" }));
    expect(within(dialog).getByLabelText("Qty Kalung Kesehatan")).toBeDefined();

    fireEvent.click(within(dialog).getByRole("button", { name: "Hapus Bio Magnetik" }));
    expect(within(dialog).queryByLabelText("Qty Bio Magnetik")).toBeNull();

    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ganti produk" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    await waitFor(() => expect(posts.some((p) => p.url === URL_REVISE)).toBe(true));
    expect(posts.find((p) => p.url === URL_REVISE)?.body).toEqual({
      alasan: "ganti produk",
      lines: [{ product_id: "pr-2", qty: 1, harga_satuan: 150000 }],
    });
  });

  it("kunci baris baru unik: produk sama ditambah ulang setelah hapus tidak saling merembet", async () => {
    const { posts } = stubPesanan({ detail: DETAIL_DONE_UJI });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();

    const pilih = await within(dialog).findByLabelText("Tambah produk");
    await waitFor(() => expect(within(pilih).getByRole("option", { name: /Kalung Kesehatan/ })).toBeDefined());

    // Tambah Kalung (panjang 2), lalu hapus Bio Magnetik supaya panjang
    // kembali 1 — persis keadaan yang dulu membuat kunci `baru-…-${length}`
    // terpakai ulang saat produk yang sama ditambahkan lagi.
    fireEvent.change(pilih, { target: { value: "pr-2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Tambah baris" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Hapus Bio Magnetik" }));

    fireEvent.change(within(dialog).getByLabelText("Tambah produk"), { target: { value: "pr-2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Tambah baris" }));

    const awal = within(dialog).getAllByLabelText("Qty Kalung Kesehatan");
    expect(awal.length).toBe(2);

    // Mengubah SATU baris tidak boleh mengubah baris kembarannya.
    fireEvent.change(awal[0], { target: { value: "7" } });
    const setelahUbah = within(dialog).getAllByLabelText("Qty Kalung Kesehatan");
    expect((setelahUbah[0] as HTMLInputElement).value).toBe("7");
    expect((setelahUbah[1] as HTMLInputElement).value).toBe("1");

    // Menghapus SATU baris hanya menghapus baris itu (dulu keduanya terhapus).
    fireEvent.click(within(dialog).getAllByRole("button", { name: "Hapus Kalung Kesehatan" })[0]);
    const tersisa = within(dialog).getAllByLabelText("Qty Kalung Kesehatan");
    expect(tersisa.length).toBe(1);
    expect((tersisa[0] as HTMLInputElement).value).toBe("1");

    // Payload tetap satu baris produk_id serta tidak pernah dikirim dua kali.
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ganti produk" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));
    await waitFor(() => expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1));
    expect(posts.find((p) => p.url === URL_REVISE)?.body).toEqual({
      alasan: "ganti produk",
      lines: [{ product_id: "pr-2", qty: 1, harga_satuan: 150000 }],
    });
  });

  it("qty/harga negatif atau desimal ditolak tanpa dikirim (bukan diam-diam diubah)", async () => {
    const { posts } = stubPesanan({ detail: DETAIL_DONE_UJI });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    const kirim = () => within(dialog).getByRole("button", { name: "Simpan revisi" });
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ubah qty" } });

    // "-5" tidak boleh menjadi 5.
    fireEvent.change(within(dialog).getByLabelText("Qty Bio Magnetik"), { target: { value: "-5" } });
    fireEvent.click(kirim());
    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toMatch(/Qty baris .*bilangan bulat minimal 1/),
    );
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(0);

    // "1.5" tidak boleh menjadi 15.
    fireEvent.change(within(dialog).getByLabelText("Qty Bio Magnetik"), { target: { value: "1.5" } });
    fireEvent.click(kirim());
    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toMatch(/Qty baris .*bilangan bulat minimal 1/),
    );
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(0);

    // qty sah, harga "-100" ditolak (bukan menjadi 100).
    fireEvent.change(within(dialog).getByLabelText("Qty Bio Magnetik"), { target: { value: "2" } });
    fireEvent.change(within(dialog).getByLabelText("Harga satuan Bio Magnetik"), { target: { value: "-100" } });
    fireEvent.click(kirim());
    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toMatch(/Harga satuan .*non-negatif/),
    );
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(0);

    // harga "1.5" ditolak.
    fireEvent.change(within(dialog).getByLabelText("Harga satuan Bio Magnetik"), { target: { value: "1.5" } });
    fireEvent.click(kirim());
    await waitFor(() =>
      expect(within(dialog).getByRole("alert").textContent).toMatch(/Harga satuan .*non-negatif/),
    );
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(0);

    // Nilai sah: dikirim sekali dengan angka apa adanya.
    fireEvent.change(within(dialog).getByLabelText("Harga satuan Bio Magnetik"), { target: { value: "120000" } });
    fireEvent.click(kirim());
    await waitFor(() => expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1));
    expect(posts.find((p) => p.url === URL_REVISE)?.body).toEqual({
      alasan: "ubah qty",
      lines: [{ line_no: 1, qty: 2, harga_satuan: 120000 }],
    });
  });

  it("409 INVOICE_NOT_READY: pesan jelas, form bertahan, refresh tanpa versi semu; retry manual lalu 200", async () => {
    const { posts, panggilan } = stubPesanan({
      detail: DETAIL_DONE_UJI,
      revise: [
        {
          status: 409,
          code: "INVOICE_NOT_READY",
          message:
            "Invoice versi ini belum selesai dirender sehingga revisi belum dapat dilakukan. Coba lagi setelah proses dokumennya selesai.",
        },
        { status: 200 },
      ],
    });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "salah harga jual" } });
    fireEvent.change(within(dialog).getByLabelText("Qty Bio Magnetik"), { target: { value: "5" } });

    const sebelum = panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length;
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    const pesan = await screen.findByRole("alert");
    expect(pesan.textContent).toMatch(/belum selesai dirender/i);
    expect(pesan.textContent).toMatch(/kirim ulang/i);

    // Tidak ada klaim sukses / versi palsu: modal & isian bertahan apa adanya.
    expect(screen.getByRole("dialog")).toBeDefined();
    expect((within(dialog).getByLabelText("Alasan revisi") as HTMLTextAreaElement).value).toBe("salah harga jual");
    expect((within(dialog).getByLabelText("Qty Bio Magnetik") as HTMLInputElement).value).toBe("5");
    expect(screen.queryByText("Revisi R1")).toBeNull();

    // Detail & dokumen dimuat ulang supaya status dokumen terkini terlihat.
    await waitFor(() => {
      expect(panggilan.filter((u) => u.endsWith(`/admin/orders/${DETAIL_ID}`)).length).toBeGreaterThan(sebelum);
    });

    // Dokumen sudah siap: pengajuan ulang MANUAL diterima 200 dan modal tertutup.
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));
    await waitFor(() => expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(2));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("409 ORDER_NOT_DONE dijelaskan terpisah dari INVOICE_NOT_READY", async () => {
    const { posts } = stubPesanan({
      detail: DETAIL_DONE_UJI,
      revise: [
        {
          status: 409,
          code: "ORDER_NOT_DONE",
          message: "Revisi hanya untuk pesanan yang sudah selesai (Done).",
        },
      ],
    });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ubah qty" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    const pesan = await screen.findByRole("alert");
    expect(pesan.textContent).toMatch(/sudah selesai \(Done\)/);
    expect(screen.queryByText("Revisi R1")).toBeNull();
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1);
  });

  it("403 (Manager saat manager_boleh_revisi nonaktif) dijelaskan; tidak berujung sukses", async () => {
    const { posts } = stubPesanan({
      detail: DETAIL_DONE_UJI,
      revise: [
        {
          status: 403,
          code: "FORBIDDEN",
          message: "Manager tidak diizinkan merevisi invoice saat ini (setelan manager_boleh_revisi nonaktif).",
        },
      ],
    });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ubah qty" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    const pesan = await screen.findByRole("alert");
    expect(pesan.textContent).toMatch(/tidak diizinkan merevisi/i);
    expect(screen.getByRole("dialog")).toBeDefined();
    expect(screen.queryByText("Revisi R1")).toBeNull();
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1);
  });

  it("400 menempelkan pesan field ke input terkait", async () => {
    const { posts } = stubPesanan({
      detail: DETAIL_DONE_UJI,
      revise: [
        {
          status: 400,
          code: "VALIDATION_FAILED",
          message: "Alasan revisi wajib diisi.",
          fields: { alasan: "alasan revisi maksimal 500 karakter" },
        },
      ],
    });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "terlalu panjang" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    expect(await within(dialog).findByText("alasan revisi maksimal 500 karakter")).toBeDefined();
    expect(screen.getByRole("dialog")).toBeDefined();
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1);
  });

  it("503 (penjadwal invoice belum siap) dijelaskan tanpa klaim sukses", async () => {
    const { posts } = stubPesanan({
      detail: DETAIL_DONE_UJI,
      revise: [
        {
          status: 503,
          code: "UNAVAILABLE",
          message: "Penjadwal pekerjaan invoice belum siap sehingga revisi belum dapat diselesaikan.",
        },
      ],
    });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ubah qty" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    const pesan = await screen.findByRole("alert");
    expect(pesan.textContent).toMatch(/belum siap/i);
    expect(screen.getByRole("dialog")).toBeDefined();
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1);
  });

  it("alasan wajib: kosong tidak pernah dikirim ke server", async () => {
    const { posts } = stubPesanan({ detail: DETAIL_DONE_UJI });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    expect((await within(dialog).findAllByText(/Alasan revisi wajib/i)).length).toBeGreaterThan(0);
    expect(posts.some((p) => p.url === URL_REVISE)).toBe(false);
  });

  it("mencegah klik ganda: tombol terkunci selama permintaan berjalan", async () => {
    let lepaskan: () => void = () => undefined;
    const tertahan = new Promise<void>((resolve) => {
      lepaskan = resolve;
    });
    const { posts } = stubPesanan({ detail: DETAIL_DONE_UJI, reviseTertahan: tertahan });
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(await screen.findByRole("button", { name: "Revisi invoice" }));
    const dialog = await bukaRevisi();
    fireEvent.change(within(dialog).getByLabelText("Alasan revisi"), { target: { value: "ubah qty" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Simpan revisi" }));

    const menunggu = await screen.findByRole("button", { name: /Menyimpan/ });
    expect(menunggu.hasAttribute("disabled")).toBe(true);
    fireEvent.click(menunggu);
    expect(posts.filter((p) => p.url === URL_REVISE).length).toBe(1);

    lepaskan();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
