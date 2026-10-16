import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionProvider } from "../session";
import { ImporImpactPage, type PreviewImpor } from "./impor-impact";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolver) => {
    resolve = resolver;
  });
  return { promise, resolve };
}

const kosong = { insert: 0, update: 0, skip: 0, conflict: 0, needs_review: 0 };

function previewUji(items: PreviewImpor["items"]): PreviewImpor {
  return {
    attempt_id: "attempt-ui-12345678",
    artifact_id: "artifact-1",
    source_hash: "a".repeat(64),
    preview_hash: "b".repeat(64),
    contract_version: "impact-master-v1",
    sheet: "Master Tarif (Opsi A)",
    created_at: "2026-10-16T12:00:00Z",
    counts: {
      branch: { ...kosong, insert: 32, update: 1, conflict: 1 },
      product: { ...kosong, insert: 13 },
      variant: { ...kosong, insert: 16 },
      tariff: { ...kosong, insert: 200, update: 4, skip: 238 },
      note: { ...kosong, needs_review: 1 },
      total: { ...kosong, insert: 261, update: 5, skip: 238, conflict: 1, needs_review: 1 },
    },
    items,
  };
}

function renderSuperadmin() {
  return render(
    <SessionProvider
      penggunaAwal={{ id: "sa-1", kind: "admin", role: "superadmin", nama: "Super Admin", email: "sa@example.test" }}
    >
      <ImporImpactPage />
    </SessionProvider>,
  );
}

function pilihWorkbook() {
  const file = new File(["fixture sintetis"], "impact-sintetis.xlsx", {
    type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  });
  fireEvent.change(screen.getByLabelText("Workbook Impact (.xlsx)"), { target: { files: [file] } });
  fireEvent.click(screen.getByRole("button", { name: "Preview workbook" }));
}

describe("ImporImpactPage", () => {
  beforeEach(() => {
    document.cookie = "grahita_csrf=uji-csrf; path=/";
  });

  afterEach(() => {
    document.cookie = "grahita_csrf=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT";
  });

  it("preview → keputusan per-item → apply staging tanpa aksi produksi", async () => {
    const preview = previewUji([
      {
        source_row: 3,
        entity_kind: "branch",
        entity_code: "fingerprint-cabang",
        fingerprint: "fp-branch",
        status: "conflict",
        reason_code: "existing_access_identity_blank",
      },
      {
        source_row: 3,
        entity_kind: "note",
        entity_code: "AR",
        fingerprint: "fp-note",
        status: "needs_review",
        reason_code: "manual_review_required",
      },
    ]);
    const panggilan: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string, init?: RequestInit) => {
        panggilan.push({ url: input, init });
        if (input.endsWith("/preview")) return json({ data: preview }, 201);
        if (input.endsWith("/evidence")) {
          return json({
            data: [
              { source_row: 3, entity_kind: "note", entity_code: "AR", value: "catatan sintetis privat" },
            ],
          });
        }
        if (input.endsWith("/apply")) {
          return json({
            data: {
              apply_run_id: "apply-1",
              attempt_id: preview.attempt_id,
              status: "applied",
              mutations: { branch: 32, product: 13, variant: 16, tariff: 204, identity_revocation: 0 },
              idempotent: false,
              applied_at: "2026-10-16T12:01:00Z",
            },
          });
        }
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();

    expect(await screen.findByText("Cabang terhitung: 34")).toBeDefined();
    expect(screen.getByText("Tarif terhitung: 442")).toBeDefined();
    expect(screen.queryByRole("button", { name: /produksi/i })).toBeNull();

    const apply = screen.getByRole("button", { name: "Setujui & apply ke staging" });
    expect(apply.hasAttribute("disabled")).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Tampilkan detail privat" }));
    expect(screen.getByText(/catatan sintetis privat/)).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Konfirmasi detail sudah ditinjau" }));
    fireEvent.change(screen.getByLabelText("Keputusan baris 3 branch"), {
      target: { value: "preserve_existing_verified_identity" },
    });
    fireEvent.change(screen.getByLabelText("Alasan baris 3 branch"), {
      target: { value: "Identitas existing sudah diverifikasi melalui workflow terpisah" },
    });
    fireEvent.change(screen.getByLabelText("Keputusan baris 3 note"), {
      target: { value: "ignored_with_reason" },
    });
    fireEvent.change(screen.getByLabelText("Alasan baris 3 note"), {
      target: { value: "Tidak memengaruhi master atau tarif" },
    });

    expect(apply.hasAttribute("disabled")).toBe(false);
    fireEvent.click(apply);

    expect(await screen.findByText(/Apply staging selesai/)).toBeDefined();
    const requestApply = panggilan.find((p) => p.url.endsWith("/apply"));
    expect((requestApply?.init?.headers as Record<string, string>)["X-CSRF-Token"]).toBe("uji-csrf");
    const body = JSON.parse(String(requestApply?.init?.body)) as { decisions: Array<{ decision: string; reason: string }> };
    expect(body.decisions).toEqual([
      expect.objectContaining({ decision: "preserve_existing_verified_identity" }),
      expect.objectContaining({ decision: "ignored_with_reason" }),
    ]);
  });

  it("mengunci apply saat source_corrected dipilih", async () => {
    const preview = previewUji([
      {
        source_row: 4,
        entity_kind: "note",
        entity_code: "AU",
        fingerprint: "fp-note-2",
        status: "needs_review",
        reason_code: "manual_review_required",
      },
    ]);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/preview")) return json({ data: preview }, 201);
        if (input.endsWith("/evidence")) {
          return json({
            data: [{ source_row: 4, entity_kind: "note", entity_code: "AU", value: "nilai sintetis" }],
          });
        }
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();
    await screen.findByText("Cabang terhitung: 34");
    fireEvent.click(screen.getByRole("button", { name: "Tampilkan detail privat" }));
    fireEvent.click(screen.getByRole("button", { name: "Konfirmasi detail sudah ditinjau" }));
    fireEvent.change(screen.getByLabelText("Keputusan baris 4 note"), {
      target: { value: "source_corrected" },
    });
    fireEvent.change(screen.getByLabelText("Alasan baris 4 note"), {
      target: { value: "Workbook diperbaiki oleh pemilik sumber" },
    });

    expect(screen.getByText(/Ada item berkeputusan “sumber sudah dikoreksi”/)).toBeDefined();
    expect(screen.getByRole("button", { name: "Setujui & apply ke staging" }).hasAttribute("disabled")).toBe(true);
  });

  it.each([
    ["kosong", []],
    [
      "parsial",
      [{ source_row: 6, entity_kind: "note", entity_code: "AR", value: "detail pertama" }],
    ],
    [
      "nilai kosong",
      [
        { source_row: 6, entity_kind: "note", entity_code: "AR", value: "detail pertama" },
        { source_row: 6, entity_kind: "note", entity_code: "AU", value: "   " },
      ],
    ],
    [
      "duplikat",
      [
        { source_row: 6, entity_kind: "note", entity_code: "AR", value: "detail pertama" },
        { source_row: 6, entity_kind: "note", entity_code: "AU", value: "detail kedua" },
        { source_row: 6, entity_kind: "note", entity_code: "AU", value: "detail duplikat" },
      ],
    ],
  ])("menahan keputusan untuk evidence HTTP 200 %s", async (_kasus, data) => {
    const preview = previewUji([
      {
        source_row: 6,
        entity_kind: "note",
        entity_code: "AR",
        fingerprint: "fp-note-ar",
        status: "needs_review",
        reason_code: "manual_review_required",
      },
      {
        source_row: 6,
        entity_kind: "note",
        entity_code: "AU",
        fingerprint: "fp-note-au",
        status: "needs_review",
        reason_code: "manual_review_required",
      },
    ]);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/preview")) return json({ data: preview }, 201);
        if (input.endsWith("/evidence")) return json({ data });
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();

    expect(await screen.findByText(/Detail privat tidak lengkap, kosong, atau duplikat/)).toBeDefined();
    expect(screen.getAllByLabelText("Keputusan baris 6 note")[0]?.hasAttribute("disabled")).toBe(true);
    expect(screen.getAllByText(/Detail privat item ini belum tersedia secara sah/)).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Setujui & apply ke staging" }).hasAttribute("disabled")).toBe(true);
  });

  it("mengabaikan respons preview file lama setelah workbook diganti", async () => {
    const previewLama = deferred<Response>();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/preview")) return previewLama.promise;
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();
    const fileBaru = new File(["fixture B"], "impact-sintetis-b.xlsx", {
      type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    });
    fireEvent.change(screen.getByLabelText("Workbook Impact (.xlsx)"), { target: { files: [fileBaru] } });
    previewLama.resolve(json({ data: previewUji([]) }, 201));

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Preview workbook" }).hasAttribute("disabled")).toBe(false),
    );
    expect(screen.queryByText("Cabang terhitung: 34")).toBeNull();
    expect(screen.queryByText(/attempt-ui-12345678/)).toBeNull();
  });

  it("mengunci file dan preview selama apply tertunda", async () => {
    const preview = previewUji([]);
    const applyTertunda = deferred<Response>();
    let jumlahPreview = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/preview")) {
          jumlahPreview += 1;
          return json({ data: preview }, 201);
        }
        if (input.endsWith("/apply")) return applyTertunda.promise;
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();
    await screen.findByText("Cabang terhitung: 34");
    fireEvent.click(screen.getByRole("button", { name: "Setujui & apply ke staging" }));

    expect(screen.getByLabelText("Workbook Impact (.xlsx)").hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Buat preview baru" }).hasAttribute("disabled")).toBe(true);
    const fileBaru = new File(["fixture B"], "impact-sintetis-b.xlsx", {
      type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    });
    fireEvent.change(screen.getByLabelText("Workbook Impact (.xlsx)"), { target: { files: [fileBaru] } });
    fireEvent.click(screen.getByRole("button", { name: "Buat preview baru" }));
    expect(screen.getByText(/Attempt attempt-ui-1234567…/)).toBeDefined();
    expect(jumlahPreview).toBe(1);

    applyTertunda.resolve(
      json({
        data: {
          apply_run_id: "apply-tertunda",
          attempt_id: preview.attempt_id,
          status: "applied",
          mutations: { branch: 0, product: 0, variant: 0, tariff: 0, identity_revocation: 0 },
          idempotent: false,
          applied_at: "2026-10-16T12:02:00Z",
        },
      }),
    );
    expect(await screen.findByText(/Apply staging selesai/)).toBeDefined();
    expect(jumlahPreview).toBe(1);
  });

  it("menahan identity remap yang tidak boleh diputus importer", async () => {
    const preview = previewUji([
      {
        source_row: 5,
        entity_kind: "branch",
        entity_code: "fingerprint-remap",
        fingerprint: "fp-remap",
        status: "conflict",
        reason_code: "identity_remap_required",
      },
    ]);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/preview")) return json({ data: preview }, 201);
        if (input.endsWith("/evidence")) return json({ data: [] });
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();

    expect(await screen.findByText(/Konflik ini tidak dapat diputus oleh importer/)).toBeDefined();
    expect(screen.getByRole("button", { name: "Setujui & apply ke staging" }).hasAttribute("disabled")).toBe(true);
    expect(screen.queryByLabelText("Keputusan baris 5 branch")).toBeNull();
  });

  it("menampilkan 409 stale, mengunci submit, dan menyediakan preview ulang", async () => {
    const preview = previewUji([]);
    let jumlahPreview = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/preview")) {
          jumlahPreview += 1;
          return json({ data: { ...preview, attempt_id: `attempt-${jumlahPreview}` } }, 201);
        }
        if (input.endsWith("/evidence")) return json({ data: [] });
        if (input.endsWith("/apply")) {
          return json(
            { error: { code: "IMPORT_PREVIEW_STALE", message: "Data master berubah." } },
            409,
          );
        }
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    renderSuperadmin();
    pilihWorkbook();
    await screen.findByText("Cabang terhitung: 34");
    fireEvent.click(screen.getByRole("button", { name: "Setujui & apply ke staging" }));

    expect(await screen.findByText(/Preview kedaluwarsa/)).toBeDefined();
    expect(screen.getByRole("button", { name: "Setujui & apply ke staging" }).hasAttribute("disabled")).toBe(true);
    const kartuPreview = screen.getByText("1. Buat preview").closest("div");
    expect(kartuPreview).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Buat preview baru" }));
    await waitFor(() => expect(jumlahPreview).toBe(2));
    expect(screen.queryByText(/Preview kedaluwarsa/)).toBeNull();
  });

  it("guard halaman menolak Manager", () => {
    render(
      <SessionProvider
        penggunaAwal={{ id: "m-1", kind: "admin", role: "manager", nama: "Manager", email: "m@example.test" }}
      >
        <ImporImpactPage />
      </SessionProvider>,
    );
    expect(screen.getByText("Tidak berwenang")).toBeDefined();
    expect(within(screen.getByText("Tidak berwenang").parentElement!).getByText(/khusus Superadmin/)).toBeDefined();
  });
});
