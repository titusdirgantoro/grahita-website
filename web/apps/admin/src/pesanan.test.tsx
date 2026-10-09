import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import type { FilterPesanan, PesananDetail, PesananHalaman } from "./pages/pesanan";

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
      satuan: "pcs",
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

interface StubUji {
  panggilan: string[];
  posts: { url: string; body: unknown }[];
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
}): StubUji {
  const panggilan: string[] = [];
  const posts: { url: string; body: unknown }[] = [];
  const total = opsi?.total ?? 1;

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
        posts.push({ url, body: init.body ? JSON.parse(String(init.body)) : null });
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
        return json({ data: {} }, 201);
      }
      if (url.includes("/admin/branches")) {
        return json({ data: [{ id: "b1", nama: "Grahita Bandung", is_active: true }] });
      }
      if (url.includes("/download")) {
        return json({
          data: { url: "https://files.test/bukti.png", expires_in: 300, nama_file: "bukti.png", mime_type: "image/png" },
        });
      }
      if (url.includes(`/admin/orders/${DETAIL_ID}/proofs`)) {
        return json({
          data: [
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
    expect(kirim?.body).toEqual({ hasil: "cocok", total_terbaca: 200000 });

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
    });
  });

  it("tanpa nominal terbaca hanya mengirim hasil (bukti non-OCR / tunai C&C)", async () => {
    const { posts } = stubPesanan();
    render(<RouterProvider router={buatRouter(`/pesanan/${DETAIL_ID}`)} />);
    await screen.findByText("Bio Magnetik");

    fireEvent.click(screen.getByRole("button", { name: "Verifikasi" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Setujui & selesaikan" }));

    await waitFor(() => {
      expect(posts.some((p) => p.url === URL_VERIFY)).toBe(true);
    });
    expect(posts.find((p) => p.url === URL_VERIFY)?.body).toEqual({ hasil: "cocok" });
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
