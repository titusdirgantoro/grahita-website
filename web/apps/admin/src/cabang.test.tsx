import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

/** CABANG meniru GET /admin/branches (master cabang, PRD §4.4). */
const CABANG = [
  {
    id: "br-a",
    nama: "Grahita Bandung",
    nama_kepala: "Sri Wahyuni",
    email: "bandung@example.test",
    telepon: "+6281200000001",
    is_cnc: true,
    is_special: false,
    pajak_in: false,
    is_active: true,
  },
  {
    id: "br-b",
    nama: "Grahita Bekasi",
    nama_kepala: "Agus Pratama",
    email: "bekasi@example.test",
    telepon: "",
    is_cnc: false,
    is_special: true,
    pajak_in: true,
    is_active: true,
    token_prefix: "GRH-7K4Q",
    token_expires_at: "2026-10-22T03:00:00Z",
  },
  {
    id: "br-c",
    nama: "Grahita Cimahi",
    nama_kepala: "Hendra",
    email: "cimahi@example.test",
    telepon: "",
    is_cnc: false,
    is_special: false,
    pajak_in: false,
    is_active: false,
  },
];

/** AKUN meniru GET /admin/users/branch: satu baris per cabang (F1-05). */
const AKUN = [
  { branch_id: "br-a", has_account: true, status: "aktif" },
  { branch_id: "br-b", has_account: false, status: "belum_registrasi" },
  { branch_id: "br-c", has_account: false, status: "belum_registrasi" },
];

const salinKlip = vi.fn(async () => undefined);

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

/**
 * stubFetch memasang fetch tiruan untuk halaman /cabang: sesi admin, master
 * cabang, dan peta akun cabang. `tambahan` menimpa respons tertentu (mis. 409).
 */
function stubFetch(
  tambahan?: (url: string, init?: RequestInit) => Response | undefined,
): Panggilan[] {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      const timpa = tambahan?.(input, init);
      if (timpa) return timpa;
      if (input.endsWith("/auth/me")) {
        return json({
          data: {
            user: {
              id: "a1",
              kind: "admin",
              role: "manager",
              nama: "Budi Santoso",
              email: "budi@example.test",
            },
          },
        });
      }
      if (input.includes("/admin/users/branch")) return json({ data: AKUN });
      if (input.includes("/admin/branches")) return json({ data: CABANG });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

/** baris mengambil <tr> yang memuat nama cabang. */
function baris(nama: string): HTMLElement {
  const sel = screen.getByText(nama).closest("tr");
  if (!sel) throw new Error(`baris ${nama} tidak ditemukan`);
  return sel;
}

beforeEach(() => {
  // Cookie CSRF double-submit dibaca klien untuk setiap mutasi.
  document.cookie = "grahita_csrf=token-uji";
  // jsdom tidak menyediakan papan klip; tombol salin token diuji lewat tiruan ini.
  salinKlip.mockClear();
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText: salinKlip },
    configurable: true,
  });
});

describe("Admin Portal — /cabang (F1-06b)", () => {
  it("Manager melihat master cabang dengan flag, status akun, dan tab filter", async () => {
    stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);

    expect(await screen.findByText("Grahita Bandung")).toBeDefined();
    expect(screen.getByText("Grahita Bekasi")).toBeDefined();
    expect(screen.getByText("Grahita Cimahi")).toBeDefined();

    // Flag tampil sebagai badge (prototype: C&C, Special, PI).
    expect(screen.getAllByText("C&C").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Special").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("PI").length).toBeGreaterThanOrEqual(1);

    // Kolom Akun: satu cabang sudah berakun, dua belum registrasi.
    expect(screen.getAllByText("Aktif").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Belum registrasi").length).toBe(2);

    // Tab filter tersedia; ringkasan header menyebut jumlah cabang + akun aktif.
    for (const label of ["Semua", "C&C", "Special", "Nonaktif"]) {
      expect(screen.getByRole("button", { name: label })).toBeDefined();
    }
    expect(screen.getByText(/3 cabang · 1 akun aktif/)).toBeDefined();
    expect(screen.queryByRole("button", { name: "Kelola rekening tujuan" })).toBeNull();
  });

  it("Superadmin menyimpan replacement assignment lengkap termasuk rekening dormant", async () => {
    const panggilan = stubFetch((url, init) => {
      if (url.endsWith("/auth/me")) {
        return json({ data: { user: { id: "sa-1", kind: "admin", role: "superadmin", nama: "Sandi", email: "sa@test" } } });
      }
      if (url.endsWith("/admin/branches/br-a/bank-accounts") && init?.method === "PUT") {
        return json({ data: { branch_id: "br-a", version: 5, bank_account_ids: ["ba-1", "ba-2", "ba-3"] } });
      }
      if (url.endsWith("/admin/branches/br-a/bank-accounts")) {
        return json({ data: {
          branch_id: "br-a",
          version: 4,
          items: [
            { id: "ba-1", bank: "BCA", nama_pemilik: "PT A", display_nomor: "••••1111", is_active: true, is_allowed: true, dormant: false, effective: true },
            { id: "ba-2", bank: "BRI", nama_pemilik: "PT B", display_nomor: "••••2222", is_active: false, is_allowed: true, dormant: true, effective: false },
            { id: "ba-3", bank: "BCA", nama_pemilik: "PT C", display_nomor: "••••3333", is_active: true, is_allowed: false, dormant: false, effective: false },
          ],
        } });
      }
      return undefined;
    });

    render(<RouterProvider router={buatRouter("/cabang")} />);
    const row = await waitFor(() => baris("Grahita Bandung"));
    fireEvent.click(within(row).getByRole("button", { name: "Kelola rekening tujuan" }));
    expect(await screen.findByText("Dormant")).toBeDefined();
    expect(screen.getByLabelText("Pilih BRI ••••2222").hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByLabelText("Pilih BCA ••••3333"));
    fireEvent.click(screen.getByRole("button", { name: "Simpan assignment" }));

    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/branches/br-a/bank-accounts") && p.init?.method === "PUT");
      expect(JSON.parse(String(put?.init?.body))).toEqual({
        expected_version: 4,
        bank_account_ids: ["ba-1", "ba-2", "ba-3"],
      });
    });
  });

  it("meminta konfirmasi khusus sebelum mencabut assignment dormant", async () => {
    const konfirmasi = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const panggilan = stubFetch((url, init) => {
      if (url.endsWith("/auth/me")) return json({ data: { user: { id: "sa-1", kind: "admin", role: "superadmin", nama: "Sandi", email: "sa@test" } } });
      if (url.endsWith("/admin/branches/br-a/bank-accounts") && init?.method === "PUT") return json({ data: { branch_id: "br-a", version: 5, bank_account_ids: ["ba-1"] } });
      if (url.endsWith("/admin/branches/br-a/bank-accounts")) return json({ data: { branch_id: "br-a", version: 4, items: [
        { id: "ba-1", bank: "BCA", nama_pemilik: "PT A", display_nomor: "••••1111", is_active: true, is_allowed: true, dormant: false, effective: true },
        { id: "ba-2", bank: "BRI", nama_pemilik: "PT B", display_nomor: "••••2222", is_active: false, is_allowed: true, dormant: true, effective: false },
      ] } });
      return undefined;
    });
    render(<RouterProvider router={buatRouter("/cabang")} />);
    fireEvent.click(within(await waitFor(() => baris("Grahita Bandung"))).getByRole("button", { name: "Kelola rekening tujuan" }));
    const cabut = await screen.findByRole("button", { name: "Cabut assignment" });
    fireEvent.click(cabut);
    expect((screen.getByLabelText("Pilih BRI ••••2222") as HTMLInputElement).checked).toBe(true);
    fireEvent.click(cabut);
    expect((screen.getByLabelText("Pilih BRI ••••2222") as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Simpan assignment" }));
    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/branches/br-a/bank-accounts") && p.init?.method === "PUT");
      expect(JSON.parse(String(put?.init?.body))).toEqual({ expected_version: 4, bank_account_ids: ["ba-1"] });
    });
    expect(konfirmasi).toHaveBeenCalledTimes(2);
    konfirmasi.mockRestore();
  });

  it("tab filter dan pencarian dikirim ke server lewat query API", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "C&C" }));
    await waitFor(() =>
      expect(panggilan.some((p) => p.url.includes("/admin/branches?cnc=true"))).toBe(true),
    );

    fireEvent.change(screen.getByLabelText("Cari cabang"), { target: { value: "bandung" } });
    await waitFor(() =>
      expect(
        panggilan.some((p) => p.url.includes("q=bandung") && p.url.includes("cnc=true")),
      ).toBe(true),
    );

    fireEvent.click(screen.getByRole("button", { name: "Nonaktif" }));
    await waitFor(() =>
      expect(
        panggilan.some((p) => p.url.includes("aktif=false") && !p.url.includes("cnc=true")),
      ).toBe(true),
    );
  });

  it("tambah cabang mengirim POST ber-CSRF lalu memuat ulang daftar", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah cabang/ }));
    expect(await screen.findByLabelText("Nama cabang")).toBeDefined();
    // Cabang baru: belum ada token, jadi blok token menyuruh menyimpan dulu.
    expect(screen.getByText(/Simpan cabang terlebih dahulu untuk membuat token/)).toBeDefined();

    fireEvent.change(screen.getByLabelText("Nama cabang"), { target: { value: "Grahita Solo" } });
    fireEvent.change(screen.getByLabelText("Nama kepala cabang"), { target: { value: "Budi" } });
    fireEvent.change(screen.getByLabelText("Email cabang"), {
      target: { value: "solo@example.test" },
    });
    fireEvent.click(screen.getByRole("switch", { name: "Cash and Carry" }));
    fireEvent.click(screen.getByRole("switch", { name: "Pajak In (PI)" }));
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    await waitFor(() => {
      const post = panggilan.find(
        (p) => p.url.endsWith("/admin/branches") && p.init?.method === "POST",
      );
      expect(post).toBeDefined();
      expect(JSON.parse(String(post?.init?.body))).toEqual({
        nama: "Grahita Solo",
        nama_kepala: "Budi",
        jabatan_kepala: "",
        email: "solo@example.test",
        telepon: "",
        is_cnc: true,
        is_special: false,
        pajak_in: true,
        is_active: true,
      });
      const header = post?.init?.headers as Record<string, string>;
      expect(header["X-CSRF-Token"]).toBe("token-uji");
    });

    // Daftar dimuat ulang (GET kedua) setelah penyimpanan berhasil.
    await waitFor(() =>
      expect(
        panggilan.filter((p) => p.url.includes("/admin/branches") && p.init?.method === "GET").length,
      ).toBeGreaterThan(1),
    );
  });

  it("EMAIL_TAKEN dari server ditampilkan pada input email", async () => {
    stubFetch((url, init) =>
      url.endsWith("/admin/branches") && init?.method === "POST"
        ? json(
            {
              error: {
                code: "EMAIL_TAKEN",
                message: "Email tersebut sudah dipakai cabang atau akun admin lain.",
              },
            },
            409,
          )
        : undefined,
    );

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah cabang/ }));
    fireEvent.change(await screen.findByLabelText("Nama cabang"), {
      target: { value: "Grahita Medan" },
    });
    fireEvent.change(screen.getByLabelText("Nama kepala cabang"), { target: { value: "Siti" } });
    fireEvent.change(screen.getByLabelText("Email cabang"), {
      target: { value: "bandung@example.test" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    expect(
      await screen.findByText("Email tersebut sudah dipakai cabang atau akun admin lain."),
    ).toBeDefined();
    expect(screen.getByLabelText("Email cabang").getAttribute("aria-invalid")).toBe("true");
    // Dialog tetap terbuka supaya email bisa dibetulkan.
    expect(screen.getByLabelText("Email cabang")).toBeDefined();
  });

  it("validasi form menahan email salah format tanpa memanggil server", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah cabang/ }));
    fireEvent.change(await screen.findByLabelText("Nama cabang"), {
      target: { value: "Grahita Medan" },
    });
    fireEvent.change(screen.getByLabelText("Nama kepala cabang"), { target: { value: "Siti" } });
    fireEvent.change(screen.getByLabelText("Email cabang"), { target: { value: "bukan-email" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    expect(await screen.findByText("Format email belum benar.")).toBeDefined();
    expect(panggilan.some((p) => p.init?.method === "POST")).toBe(false);
  });

  it("token hanya untuk cabang belum berakun: tampil sekali lalu bisa disalin", async () => {
    const panggilan = stubFetch((url, init) =>
      url.endsWith("/admin/branches/br-b/token") && init?.method === "POST"
        ? json({
            data: {
              token: "GRH-7K2M-QW3Z-9TPL-XB4N",
              prefix: "GRH-7K2M",
              expires_at: "2026-10-22T03:00:00Z",
            },
          })
        : undefined,
    );

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bekasi")).toBeDefined();

    fireEvent.click(within(baris("Grahita Bekasi")).getByRole("button", { name: "Ubah" }));
    expect(await screen.findByLabelText("Nama cabang")).toBeDefined();
    // Server menyimpan token sebagai hash: hanya prefix yang bisa ditampilkan ulang.
    expect(screen.getByText(/Token aktif/)).toBeDefined();
    expect(screen.getByText(/GRH-7K4Q…/)).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /^Buat token/ }));

    expect(await screen.findByDisplayValue("GRH-7K2M-QW3Z-9TPL-XB4N")).toBeDefined();
    expect(screen.getByText(/tidak bisa ditampilkan lagi setelah dialog ditutup/)).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Salin" }));
    await waitFor(() => expect(salinKlip).toHaveBeenCalledWith("GRH-7K2M-QW3Z-9TPL-XB4N"));

    // Token dibuat lewat POST ber-CSRF ke rute token cabang itu.
    const post = panggilan.find((p) => p.url.endsWith("/admin/branches/br-b/token"));
    expect(post?.init?.method).toBe("POST");
    expect((post?.init?.headers as Record<string, string>)["X-CSRF-Token"]).toBe("token-uji");
  });

  it("cabang berakun tidak ditawari token (pemulihan lewat lupa kata sandi)", async () => {
    stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(within(baris("Grahita Bandung")).getByRole("button", { name: "Ubah" }));
    expect(await screen.findByLabelText("Nama cabang")).toBeDefined();

    expect(screen.getByText(/sudah punya akun, jadi token tidak diperlukan/)).toBeDefined();
    expect(screen.queryByRole("button", { name: /Buat token/ })).toBeNull();
  });

  it("cabang nonaktif tidak boleh membuat token registrasi", async () => {
    stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Cimahi")).toBeDefined();

    fireEvent.click(within(baris("Grahita Cimahi")).getByRole("button", { name: "Ubah" }));
    expect(await screen.findByLabelText("Nama cabang")).toBeDefined();

    expect(screen.getByText(/Cabang sedang nonaktif; aktifkan dulu/)).toBeDefined();
    expect(screen.queryByRole("button", { name: /Buat token/ })).toBeNull();
  });

  it("hapus minta konfirmasi; 409 BRANCH_HAS_ORDERS dialihkan ke jalur nonaktifkan", async () => {
    const panggilan = stubFetch((url, init) => {
      if (url.endsWith("/admin/branches/br-a") && init?.method === "DELETE") {
        return json(
          {
            error: {
              code: "BRANCH_HAS_ORDERS",
              message:
                "Cabang ini sudah punya pesanan sehingga tidak bisa dihapus; nonaktifkan saja cabangnya.",
            },
          },
          409,
        );
      }
      if (url.endsWith("/admin/branches/br-a") && init?.method === "PUT") {
        return json({ data: { ...CABANG[0], is_active: false } });
      }
      return undefined;
    });

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(within(baris("Grahita Bandung")).getByRole("button", { name: "Hapus" }));
    expect(await screen.findByRole("heading", { name: "Hapus cabang" })).toBeDefined();
    expect(screen.getByText(/tidak bisa dikembalikan/)).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Hapus cabang" }));

    expect(await screen.findByText(/nonaktifkan saja cabangnya/)).toBeDefined();
    // Tombol hapus digantikan jalur nonaktifkan (AP-CAB-04).
    expect(screen.queryByRole("button", { name: "Hapus cabang" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Nonaktifkan cabang" }));
    await waitFor(() => {
      const put = panggilan.find(
        (p) => p.url.endsWith("/admin/branches/br-a") && p.init?.method === "PUT",
      );
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({ is_active: false });
    });
  });

  it("mengubah email cabang berakun minta konfirmasi 'Ya, ubah email login'", async () => {
    const panggilan = stubFetch();

    render(<RouterProvider router={buatRouter("/cabang")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(within(baris("Grahita Bandung")).getByRole("button", { name: "Ubah" }));
    fireEvent.change(await screen.findByLabelText("Email cabang"), {
      target: { value: "bandung-baru@example.test" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    expect(await screen.findByText(/Email login cabang akan berubah dari/)).toBeDefined();
    expect(panggilan.some((p) => p.init?.method === "PUT")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Ya, ubah email login" }));
    await waitFor(() => {
      const put = panggilan.find(
        (p) => p.url.endsWith("/admin/branches/br-a") && p.init?.method === "PUT",
      );
      expect(put).toBeDefined();
      const body = JSON.parse(String(put?.init?.body));
      expect(body.email).toBe("bandung-baru@example.test");
      expect(body.nama).toBe("Grahita Bandung");
    });
  });

  it("keadaan kosong dan galat memakai komponen @grahita/ui", async () => {
    stubFetch((url) =>
      url.includes("/admin/branches")
        ? json({ error: { code: "UNAVAILABLE", message: "Layanan master cabang belum tersedia." } }, 503)
        : undefined,
    );

    render(<RouterProvider router={buatRouter("/cabang")} />);

    expect(await screen.findByText("Layanan master cabang belum tersedia.")).toBeDefined();
    expect(screen.queryByText("Grahita Bandung")).toBeNull();
  });
});
