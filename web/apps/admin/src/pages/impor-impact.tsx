import { useMemo, useRef, useState, type FormEvent } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  Field,
  Form,
  Input,
  Notice,
  PageHead,
  Select,
  Table,
  Textarea,
  type BadgeTone,
} from "@grahita/ui";

import { PenjagaSuperadmin } from "./shell-pages";
import styles from "./admin-pages.module.css";

export type StatusImpor = "insert" | "update" | "skip" | "conflict" | "needs_review";
export type JenisEntitasImpor = "branch" | "product" | "variant" | "tariff" | "note";

export interface HitunganStatus {
  insert: number;
  update: number;
  skip: number;
  conflict: number;
  needs_review: number;
}

export interface PreviewItemImpor {
  source_row: number;
  entity_kind: JenisEntitasImpor;
  entity_code: string;
  fingerprint: string;
  status: StatusImpor;
  reason_code?: string;
}

export interface PreviewImpor {
  attempt_id: string;
  artifact_id: string;
  source_hash: string;
  preview_hash: string;
  contract_version: string;
  sheet: string;
  counts: Record<"branch" | "product" | "variant" | "tariff" | "note" | "total", HitunganStatus>;
  items: PreviewItemImpor[];
  created_at: string;
}

interface EvidenceImpor {
  source_row: number;
  entity_kind: JenisEntitasImpor;
  entity_code: string;
  value: string;
}

interface KeputusanImpor {
  decision: string;
  reason: string;
}

interface HasilApply {
  apply_run_id: string;
  attempt_id: string;
  status: string;
  mutations: {
    branch: number;
    product: number;
    variant: number;
    tariff: number;
    identity_revocation: number;
  };
  idempotent: boolean;
  applied_at: string;
}

const LABEL_ENTITAS: Record<string, string> = {
  branch: "Cabang",
  product: "Produk",
  variant: "Varian",
  tariff: "Tarif",
  note: "Catatan/kolom luar tarif",
  total: "Total",
};

const LABEL_STATUS: Record<StatusImpor, string> = {
  insert: "Insert",
  update: "Update",
  skip: "Skip",
  conflict: "Conflict",
  needs_review: "Needs review",
};

function kunciItem(item: Pick<PreviewItemImpor, "source_row" | "entity_kind" | "entity_code">): string {
  return `${item.source_row}|${item.entity_kind}|${item.entity_code}`;
}

function opsiKeputusan(item: PreviewItemImpor): Array<{ value: string; label: string }> {
  if (item.status === "needs_review") {
    return [
      { value: "ignored_with_reason", label: "Abaikan dengan alasan" },
      { value: "mapped_by_explicit_rule", label: "Petakan dengan aturan eksplisit" },
      { value: "source_corrected", label: "Sumber sudah dikoreksi — wajib preview ulang" },
    ];
  }
  if (item.reason_code === "existing_access_identity_blank") {
    return [
      { value: "preserve_existing_verified_identity", label: "Pertahankan identitas terverifikasi" },
      { value: "revoke_access_and_mark_incomplete", label: "Cabut akses dan tandai identitas belum lengkap" },
    ];
  }
  if (item.reason_code === "inconsistent_existing_access") {
    return [{ value: "revoke_inconsistent_access", label: "Cabut akses yang tidak konsisten" }];
  }
  return [];
}

function nadaStatus(status: StatusImpor): BadgeTone {
  switch (status) {
    case "insert":
      return "success";
    case "update":
      return "info";
    case "conflict":
      return "danger";
    case "needs_review":
      return "warning";
    default:
      return "neutral";
  }
}

function kodePendek(value: string): string {
  return value.length > 18 ? `${value.slice(0, 18)}…` : value;
}

function pesanGalat(err: unknown): string {
  return err instanceof ApiClientError ? err.message : "Terjadi kesalahan. Coba lagi.";
}

export function keputusanLengkap(item: PreviewItemImpor, keputusan?: KeputusanImpor): boolean {
  const opsi = opsiKeputusan(item);
  if (opsi.length === 0 || !keputusan || keputusan.reason.trim() === "") return false;
  if (!opsi.some((pilihan) => pilihan.value === keputusan.decision)) return false;
  return keputusan.decision !== "source_corrected";
}

export function ImporImpactPage() {
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<PreviewImpor | null>(null);
  const [evidence, setEvidence] = useState<Record<string, string>>({});
  const [terbuka, setTerbuka] = useState<Record<string, boolean>>({});
  const [evidenceDikonfirmasi, setEvidenceDikonfirmasi] = useState<Record<string, boolean>>({});
  const [keputusan, setKeputusan] = useState<Record<string, KeputusanImpor>>({});
  const [hasil, setHasil] = useState<HasilApply | null>(null);
  const [sibukPreview, setSibukPreview] = useState(false);
  const [sibukApply, setSibukApply] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const [peringatanEvidence, setPeringatanEvidence] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const fileAktifRef = useRef<File | null>(null);
  const generasiFileRef = useRef(0);
  const requestPreviewRef = useRef(0);
  const requestApplyRef = useRef(0);
  const attemptAktifRef = useRef<string | null>(null);

  const itemKeputusan = useMemo(
    () => preview?.items.filter((item) => item.status === "conflict" || item.status === "needs_review") ?? [],
    [preview],
  );
  const keputusanTersisa = itemKeputusan.filter((item) => !keputusanLengkap(item, keputusan[kunciItem(item)]));
  const adaKoreksiSumber = itemKeputusan.some(
    (item) => keputusan[kunciItem(item)]?.decision === "source_corrected",
  );
  const itemNeedsReview = itemKeputusan.filter((item) => item.status === "needs_review");
  const evidenceTersisa = itemNeedsReview.filter((item) => !evidenceDikonfirmasi[kunciItem(item)]);
  const evidenceTertahan = itemNeedsReview.length > 0 && (Boolean(peringatanEvidence) || evidenceTersisa.length > 0);
  const applyTerkunci =
    !preview ||
    stale ||
    hasil !== null ||
    keputusanTersisa.length > 0 ||
    adaKoreksiSumber ||
    evidenceTertahan ||
    sibukApply;

  function ubahKeputusan(item: PreviewItemImpor, patch: Partial<KeputusanImpor>) {
    const key = kunciItem(item);
    setKeputusan((lama) => ({
      ...lama,
      [key]: { decision: lama[key]?.decision ?? "", reason: lama[key]?.reason ?? "", ...patch },
    }));
  }

  async function buatPreview(event?: FormEvent<HTMLFormElement>) {
    event?.preventDefault();
    if (sibukApply || sibukPreview) return;
    const fileTerpilih = fileAktifRef.current;
    if (!fileTerpilih) {
      setGalat("Pilih workbook Impact berformat .xlsx terlebih dahulu.");
      return;
    }
    const requestID = ++requestPreviewRef.current;
    const generasiFile = generasiFileRef.current;
    ++requestApplyRef.current;
    attemptAktifRef.current = null;
    setSibukPreview(true);
    setGalat(null);
    setPeringatanEvidence(null);
    setStale(false);
    setHasil(null);
    try {
      const form = new FormData();
      form.append("file", fileTerpilih);
      const baru = await api.postForm<PreviewImpor>("/admin/master-imports/impact/preview", form);
      if (
        requestID !== requestPreviewRef.current ||
        generasiFile !== generasiFileRef.current ||
        fileTerpilih !== fileAktifRef.current
      ) {
        return;
      }
      attemptAktifRef.current = baru.attempt_id;
      setPreview(baru);
      setKeputusan({});
      setEvidence({});
      setTerbuka({});
      setEvidenceDikonfirmasi({});
      const expectedEvidence = baru.items.filter((item) => item.status === "needs_review");
      if (expectedEvidence.length === 0) return;
      try {
        const privat = await api.get<EvidenceImpor[]>(
          `/admin/master-imports/impact/${baru.attempt_id}/evidence`,
        );
        if (
          requestID !== requestPreviewRef.current ||
          generasiFile !== generasiFileRef.current ||
          baru.attempt_id !== attemptAktifRef.current
        ) {
          return;
        }
        const jumlahPerKunci = new Map<string, number>();
        for (const item of privat) {
          const key = kunciItem(item);
          jumlahPerKunci.set(key, (jumlahPerKunci.get(key) ?? 0) + 1);
        }
        const evidenceValid = expectedEvidence.every((item) => {
          const key = kunciItem(item);
          const detail = privat.find((candidate) => kunciItem(candidate) === key)?.value;
          return jumlahPerKunci.get(key) === 1 && typeof detail === "string" && detail.trim() !== "";
        });
        if (!evidenceValid) {
          setEvidence({});
          setPeringatanEvidence(
            "Detail privat tidak lengkap, kosong, atau duplikat. Keputusan dan apply dikunci; buat preview baru setelah evidence diperbaiki.",
          );
          return;
        }
        setEvidence(
          Object.fromEntries(
            privat
              .filter((item) => expectedEvidence.some((expected) => kunciItem(expected) === kunciItem(item)))
              .map((item) => [kunciItem(item), item.value]),
          ),
        );
      } catch {
        if (requestID !== requestPreviewRef.current || baru.attempt_id !== attemptAktifRef.current) return;
        setPeringatanEvidence(
          "Detail privat tidak dapat dimuat. Jangan memutus catatan yang belum dapat diverifikasi; buat preview baru setelah layanan pulih.",
        );
      }
    } catch (err) {
      if (
        requestID !== requestPreviewRef.current ||
        generasiFile !== generasiFileRef.current ||
        fileTerpilih !== fileAktifRef.current
      ) {
        return;
      }
      attemptAktifRef.current = null;
      setPreview(null);
      setGalat(pesanGalat(err));
    } finally {
      if (requestID === requestPreviewRef.current) setSibukPreview(false);
    }
  }

  async function applyStaging() {
    if (!preview || applyTerkunci) return;
    const attemptID = preview.attempt_id;
    const requestID = ++requestApplyRef.current;
    setSibukApply(true);
    setGalat(null);
    try {
      const decisions = itemKeputusan.map((item) => {
        const nilai = keputusan[kunciItem(item)];
        return {
          source_row: item.source_row,
          entity_kind: item.entity_kind,
          entity_code: item.entity_code,
          decision: nilai.decision,
          reason: nilai.reason.trim(),
        };
      });
      const result = await api.post<HasilApply>(
        `/admin/master-imports/impact/${attemptID}/apply`,
        {
          preview_hash: preview.preview_hash,
          idempotency_key: `impact-ui-${preview.attempt_id}-${preview.preview_hash.slice(0, 12)}`,
          decisions,
        },
      );
      if (requestID !== requestApplyRef.current || attemptID !== attemptAktifRef.current) return;
      setHasil(result);
    } catch (err) {
      if (requestID !== requestApplyRef.current || attemptID !== attemptAktifRef.current) return;
      if (err instanceof ApiClientError && err.status === 409) {
        setStale(true);
        setGalat(
          err.code === "IMPORT_PREVIEW_STALE"
            ? "Preview kedaluwarsa karena data master atau akses berubah. Buat preview baru sebelum apply staging."
            : `${err.message} Buat preview baru dan periksa kembali seluruh keputusan.`,
        );
      } else {
        setGalat(pesanGalat(err));
      }
    } finally {
      if (requestID === requestApplyRef.current) setSibukApply(false);
    }
  }

  return (
    <PenjagaSuperadmin>
      <PageHead
        title="Impor master Impact"
        sub="Preview dan approval per-item untuk staging; produksi tidak tersedia dari halaman ini"
      />
      <div className={styles.stack}>
        <Notice tone="warning">
          Workbook dan detail catatan bersifat privat. Preview tidak mengubah master. Apply hanya menuju lingkungan
          staging yang diizinkan server; tidak ada tombol apply produksi.
        </Notice>

        <Card>
          <CardHead title="1. Buat preview" />
          <Form onSubmit={(event) => void buatPreview(event)}>
            <Field
              label="Workbook Impact (.xlsx)"
              htmlFor="impact-workbook"
              hint="Server memverifikasi hash, sheet, header, dan tepat 34 cabang."
            >
              <Input
                id="impact-workbook"
                type="file"
                accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
                disabled={sibukApply}
                onChange={(event) => {
                  if (sibukApply) return;
                  const baru = event.target.files?.[0] ?? null;
                  fileAktifRef.current = baru;
                  generasiFileRef.current += 1;
                  requestPreviewRef.current += 1;
                  requestApplyRef.current += 1;
                  attemptAktifRef.current = null;
                  setFile(baru);
                  setSibukPreview(false);
                  setPreview(null);
                  setEvidence({});
                  setTerbuka({});
                  setEvidenceDikonfirmasi({});
                  setKeputusan({});
                  setHasil(null);
                  setGalat(null);
                  setPeringatanEvidence(null);
                  setStale(false);
                }}
              />
            </Field>
            <div className={styles.row}>
              <Button type="submit" variant="primary" disabled={!file || sibukPreview || sibukApply}>
                {sibukPreview ? "Membuat preview…" : preview ? "Buat preview baru" : "Preview workbook"}
              </Button>
              {preview ? <Badge tone="info">Attempt {kodePendek(preview.attempt_id)}</Badge> : null}
            </div>
          </Form>
        </Card>

        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        {peringatanEvidence ? <Notice tone="warning">{peringatanEvidence}</Notice> : null}
        {stale ? (
          <Notice tone="warning">
            Submit dikunci. Unggah ulang workbook yang sama melalui tombol “Buat preview baru”; keputusan attempt lama
            tidak diwariskan.
          </Notice>
        ) : null}

        {preview ? (
          <>
            <Card>
              <CardHead title="2. Ringkasan preview" />
              <div className={styles.row} style={{ marginBottom: 12 }}>
                <Badge tone="neutral">34 cabang wajib</Badge>
                <Badge tone={preview.counts.branch.conflict > 0 ? "danger" : "success"}>
                  Cabang terhitung: {Object.values(preview.counts.branch).reduce((a, b) => a + b, 0)}
                </Badge>
                <Badge tone={preview.counts.tariff.conflict > 0 ? "danger" : "info"}>
                  Tarif terhitung: {Object.values(preview.counts.tariff).reduce((a, b) => a + b, 0)}
                </Badge>
                <Badge tone="neutral">Sheet: {preview.sheet}</Badge>
              </div>
              <Table stack="wide" caption="Hitungan status preview per jenis entitas">
                <thead>
                  <tr>
                    <th>Entitas</th>
                    <th>Insert</th>
                    <th>Update</th>
                    <th>Skip</th>
                    <th>Conflict</th>
                    <th>Needs review</th>
                  </tr>
                </thead>
                <tbody>
                  {(["branch", "product", "variant", "tariff", "note", "total"] as const).map((kind) => (
                    <tr key={kind}>
                      <th scope="row">{LABEL_ENTITAS[kind]}</th>
                      <td>{preview.counts[kind].insert}</td>
                      <td>{preview.counts[kind].update}</td>
                      <td>{preview.counts[kind].skip}</td>
                      <td>{preview.counts[kind].conflict}</td>
                      <td>{preview.counts[kind].needs_review}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </Card>

            <Card>
              <CardHead title="3. Review dan keputusan per item" />
              <Table stack="wide" caption="Item preview impor Impact">
                <thead>
                  <tr>
                    <th>Baris</th>
                    <th>Entitas</th>
                    <th>Referensi tersamar</th>
                    <th>Status</th>
                    <th>Reason code</th>
                    <th>Keputusan</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.items.map((item) => {
                    const key = kunciItem(item);
                    const opsi = opsiKeputusan(item);
                    const nilai = keputusan[key] ?? { decision: "", reason: "" };
                    const detail = evidence[key];
                    return (
                      <tr key={key}>
                        <td data-label="Baris">{item.source_row || "—"}</td>
                        <td data-label="Entitas">{LABEL_ENTITAS[item.entity_kind]}</td>
                        <td data-label="Referensi tersamar">
                          <code title={item.entity_code}>{kodePendek(item.entity_code)}</code>
                        </td>
                        <td data-label="Status">
                          <Badge tone={nadaStatus(item.status)}>{LABEL_STATUS[item.status]}</Badge>
                        </td>
                        <td data-label="Reason code">{item.reason_code || "—"}</td>
                        <td data-label="Keputusan">
                          {item.status === "conflict" || item.status === "needs_review" ? (
                            <div className={styles.stack}>
                              {item.status === "needs_review" && detail ? (
                                <div>
                                  <Button
                                    type="button"
                                    variant="ghost"
                                    onClick={() => setTerbuka((lama) => ({ ...lama, [key]: !lama[key] }))}
                                    aria-expanded={Boolean(terbuka[key])}
                                  >
                                    {terbuka[key] ? "Sembunyikan detail privat" : "Tampilkan detail privat"}
                                  </Button>
                                  {terbuka[key] ? (
                                    <div className={styles.stack}>
                                      <Notice tone="warning">
                                        Detail privat baris {item.source_row}, kolom {item.entity_code}: {detail}
                                      </Notice>
                                      <Button
                                        type="button"
                                        variant="ghost"
                                        disabled={Boolean(evidenceDikonfirmasi[key])}
                                        onClick={() =>
                                          setEvidenceDikonfirmasi((lama) => ({ ...lama, [key]: true }))
                                        }
                                      >
                                        {evidenceDikonfirmasi[key]
                                          ? "Detail sudah dikonfirmasi"
                                          : "Konfirmasi detail sudah ditinjau"}
                                      </Button>
                                    </div>
                                  ) : null}
                                </div>
                              ) : item.status === "needs_review" ? (
                                <Notice tone="danger">Detail privat item ini belum tersedia secara sah.</Notice>
                              ) : null}
                              {opsi.length > 0 ? (
                                <>
                                  <Select
                                    aria-label={`Keputusan baris ${item.source_row} ${item.entity_kind}`}
                                    value={nilai.decision}
                                    disabled={item.status === "needs_review" && !evidenceDikonfirmasi[key]}
                                    onChange={(event) => ubahKeputusan(item, { decision: event.target.value })}
                                  >
                                    <option value="">Pilih keputusan…</option>
                                    {opsi.map((pilihan) => (
                                      <option key={pilihan.value} value={pilihan.value}>
                                        {pilihan.label}
                                      </option>
                                    ))}
                                  </Select>
                                  <Textarea
                                    aria-label={`Alasan baris ${item.source_row} ${item.entity_kind}`}
                                    placeholder="Alasan operator (privat, wajib)"
                                    rows={2}
                                    value={nilai.reason}
                                    disabled={item.status === "needs_review" && !evidenceDikonfirmasi[key]}
                                    onChange={(event) => ubahKeputusan(item, { reason: event.target.value })}
                                  />
                                </>
                              ) : (
                                <Notice tone="danger">
                                  Konflik ini tidak dapat diputus oleh importer. Perbaiki pemetaan/sumber, lalu buat
                                  preview baru.
                                </Notice>
                              )}
                            </div>
                          ) : (
                            "Tidak perlu keputusan"
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </Table>
            </Card>

            <Card>
              <CardHead title="4. Apply staging" />
              {adaKoreksiSumber ? (
                <Notice tone="warning">
                  Ada item berkeputusan “sumber sudah dikoreksi”. Apply tidak boleh dilanjutkan; buat preview baru dari
                  workbook hasil koreksi.
                </Notice>
              ) : null}
              {evidenceTertahan ? (
                <Notice tone="warning">
                  Apply dikunci karena {evidenceTersisa.length || itemNeedsReview.length} detail privat item needs review
                  belum tersedia atau belum dibuka dan dikonfirmasi.
                </Notice>
              ) : null}
              {keputusanTersisa.length > 0 ? (
                <Notice tone="warning">
                  Apply dikunci: {keputusanTersisa.length} conflict/keputusan masih belum diselesaikan secara sah.
                </Notice>
              ) : null}
              {hasil ? (
                <Notice tone="success">
                  Apply staging selesai. Run {kodePendek(hasil.apply_run_id)}; mutasi cabang {hasil.mutations.branch},
                  produk {hasil.mutations.product}, varian {hasil.mutations.variant}, tarif {hasil.mutations.tariff},
                  revokasi akses {hasil.mutations.identity_revocation}.
                </Notice>
              ) : null}
              <div className={styles.row} style={{ marginTop: 12 }}>
                <Button
                  type="button"
                  variant="primary"
                  disabled={applyTerkunci}
                  onClick={() => void applyStaging()}
                >
                  {sibukApply ? "Menerapkan ke staging…" : "Setujui & apply ke staging"}
                </Button>
                <Badge tone="danger">Produksi dilarang</Badge>
              </div>
            </Card>
          </>
        ) : null}
      </div>
    </PenjagaSuperadmin>
  );
}
