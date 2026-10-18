import { useCallback, useEffect, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  EmptyState,
  Field,
  Form,
  Input,
  Notice,
  PageHead,
  SkeletonList,
  SwitchRow,
  Table,
} from "@grahita/ui";

import { PenjagaSuperadmin } from "./shell-pages";
import styles from "./admin-pages.module.css";

/** Nilai adalah peta kunci pengaturan dari API (PRD §4.9). */
export type NilaiPengaturan = Record<string, unknown>;

const label = (kunci: string) =>
  ({
    biaya_packaging: "Biaya packaging (Rp)",
    token_hari: "Masa berlaku token registrasi (hari)",
    reset_menit: "Masa berlaku tautan reset (menit)",
    max_mb: "Ukuran maksimal bukti transfer (MB)",
    max_file: "Jumlah maksimal file bukti",
    digit_invoice: "Jumlah digit urut nomor invoice",
  })[kunci] ?? kunci;

/** Kunci bilangan bulat yang ditampilkan sebagai input angka. */
const KUNCI_ANGKA = [
  "biaya_packaging",
  "token_hari",
  "reset_menit",
  "max_mb",
  "max_file",
  "digit_invoice",
] as const;

/** Field AI (PRD §5). min_keyakinan pecahan 0..1; sisanya bilangan bulat. */
const KUNCI_AI = [
  ["min_keyakinan", "Ambang keyakinan OCR (0–1)", "0.01"],
  ["tol_kurang", "Toleransi kurang bayar (Rp)", "1"],
  ["tol_lebih", "Toleransi lebih bayar (Rp)", "1"],
  ["digit_rek", "Digit akhir rekening dicocokkan", "1"],
  ["mundur", "Toleransi mundur tanggal transfer (hari)", "1"],
] as const;

function pesanGalat(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Terjadi kesalahan. Coba lagi.";
}

function sebagaiObjek(v: unknown): Record<string, unknown> {
  return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
}

interface FormPengaturan {
  angka: Record<string, string>;
  managerRevisi: boolean;
  aiAktif: boolean;
  ai: Record<string, string>;
}

function dariApi(nilai: NilaiPengaturan): FormPengaturan {
  const angka: Record<string, string> = {};
  for (const k of KUNCI_ANGKA) {
    const v = nilai[k];
    angka[k] = v === undefined || v === null ? "" : String(v);
  }
  const ai = sebagaiObjek(nilai.ai);
  const aiStr: Record<string, string> = {};
  for (const [k] of KUNCI_AI) {
    aiStr[k] = ai[k] === undefined || ai[k] === null ? "" : String(ai[k]);
  }
  return {
    angka,
    managerRevisi: nilai.manager_boleh_revisi === true,
    aiAktif: ai.aktif === true,
    ai: aiStr,
  };
}

/**
 * PengaturanPage — /pengaturan (Superadmin): nilai pengaturan sistem
 * (PRD §2.1, §4.9) plus status cadangan terenkripsi (Architecture.md §7).
 * Semua nilai divalidasi server (tipe + rentang); pesan kesalahan per kunci
 * ditampilkan pada field yang bersangkutan. Perubahan biaya packaging tidak
 * memengaruhi pesanan lama karena harga di-snapshot pada order line saat
 * submit (BR-05, CP-SER-05).
 */
export function PengaturanPage() {
  return (
    <PenjagaSuperadmin>
      <PengaturanContent />
    </PenjagaSuperadmin>
  );
}

/** BackupRun adalah satu baris backup_runs seperti dikirim API. */
export interface BackupRun {
  id: string;
  jenis: string;
  mulai: string;
  selesai?: string;
  sukses?: boolean;
  ukuran_byte: number;
  tujuan?: string;
  pesan?: string;
}

/** RingkasanBackup adalah bentuk data GET /admin/backup-runs (F1-21). */
export interface RingkasanBackup {
  terakhir: BackupRun[];
  perlu_tindakan: boolean;
  pesan?: string;
  tujuan: string;
  jadwal: string;
  retensi: { harian: number; bulanan: number; wal_hari: number; bukti_tanpa_batas: boolean };
  sukses_terakhir?: Record<string, string>;
}

/** waktuWIB menampilkan stempel waktu dalam zona bisnis (WIB, UTC+7). */
function waktuWIB(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return new Intl.DateTimeFormat("id-ID", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "Asia/Jakarta",
  }).format(d);
}

/** ukuranRingkas menampilkan ukuran byte dalam satuan yang mudah dibaca. */
function ukuranRingkas(byte: number): string {
  if (!byte) return "—";
  if (byte < 1024) return `${byte} B`;
  if (byte < 1024 * 1024) return `${(byte / 1024).toFixed(1)} KB`;
  return `${(byte / (1024 * 1024)).toFixed(1)} MB`;
}

/** TetapanBackup menampilkan status + riwayat cadangan terenkripsi (F1-21). */
function TetapanBackup() {
  const [ringkasan, setRingkasan] = useState<RingkasanBackup | null>(null);
  const [galat, setGalat] = useState<string | null>(null);
  const [memuat, setMemuat] = useState(true);

  useEffect(() => {
    let batal = false;
    void (async () => {
      try {
        const data = await api.get<RingkasanBackup>("/admin/backup-runs");
        if (!batal) setRingkasan(data);
      } catch (err) {
        if (!batal) setGalat(pesanGalat(err));
      } finally {
        if (!batal) setMemuat(false);
      }
    })();
    return () => {
      batal = true;
    };
  }, []);

  return (
    <Card>
      <CardHead title="Backup harian" />
      {memuat ? (
        <SkeletonList rows={2} />
      ) : galat ? (
        <Notice tone="danger">{galat}</Notice>
      ) : ringkasan ? (
        <>
          {ringkasan.perlu_tindakan ? (
            <Notice tone="danger">
              {ringkasan.pesan ??
                "Cadangan terakhir gagal dan belum ada cadangan sukses sesudahnya. Hubungi operator."}
            </Notice>
          ) : null}
          <p className="muted small">
            Tujuan: {ringkasan.tujuan || "belum dikonfigurasi"} · Jadwal: {ringkasan.jadwal} · Retensi:{" "}
            {ringkasan.retensi.harian} harian, {ringkasan.retensi.bulanan} bulanan, WAL{" "}
            {ringkasan.retensi.wal_hari} hari
            {ringkasan.retensi.bukti_tanpa_batas ? ", bukti transfer tanpa batas" : ""}.
          </p>
          {ringkasan.terakhir.length === 0 ? (
            <EmptyState icon="cloud-arrow-up">
              Belum ada pekerjaan cadangan tercatat. Cadangan berjalan otomatis setiap hari pukul
              02:00 WIB.
            </EmptyState>
          ) : (
            <Table stack="narrow" caption="Riwayat cadangan">
              <thead>
                <tr>
                  <th scope="col">Mulai (WIB)</th>
                  <th scope="col">Jenis</th>
                  <th scope="col">Status</th>
                  <th scope="col">Ukuran</th>
                  <th scope="col">Tujuan / pesan</th>
                </tr>
              </thead>
              <tbody>
                {ringkasan.terakhir.map((run) => (
                  <tr key={run.id}>
                    <td className="tabular">{waktuWIB(run.mulai)}</td>
                    <td>{run.jenis}</td>
                    <td>
                      {run.sukses === undefined ? (
                        <Badge tone="warning">Berjalan</Badge>
                      ) : run.sukses ? (
                        <Badge tone="success">Sukses</Badge>
                      ) : (
                        <Badge tone="danger">Gagal</Badge>
                      )}
                    </td>
                    <td className="tabular">{ukuranRingkas(run.ukuran_byte)}</td>
                    <td className="muted small">
                      {run.tujuan || "—"}
                      {run.pesan ? ` — ${run.pesan}` : ""}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </>
      ) : null}
    </Card>
  );
}

function PengaturanContent() {
  const [nilai, setNilai] = useState<NilaiPengaturan | null>(null);
  const [awal, setAwal] = useState<FormPengaturan | null>(null);
  const [form, setForm] = useState<FormPengaturan | null>(null);
  const [memuat, setMemuat] = useState(true);
  const [galat, setGalat] = useState<string | null>(null);
  const [galatForm, setGalatForm] = useState<string | null>(null);
  const [errorFields, setErrorFields] = useState<Record<string, string>>({});
  const [sibuk, setSibuk] = useState(false);
  const [sukses, setSukses] = useState<string | null>(null);

  const muat = useCallback(async () => {
    setMemuat(true);
    try {
      const data = await api.get<NilaiPengaturan>("/admin/settings");
      setNilai(data);
      const f = dariApi(data);
      setAwal(f);
      setForm(f);
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, []);

  useEffect(() => {
    void muat();
  }, [muat]);

  /** changes menyusun peta perubahan dari field yang benar-benar diubah. */
  function changes(): Record<string, unknown> {
    if (!form || !awal) return {};
    const out: Record<string, unknown> = {};
    for (const k of KUNCI_ANGKA) {
      if (form.angka[k] !== awal.angka[k] && form.angka[k] !== "") out[k] = Number(form.angka[k]);
    }
    if (form.managerRevisi !== awal.managerRevisi) out.manager_boleh_revisi = form.managerRevisi;
    const aiBerubah =
      form.aiAktif !== awal.aiAktif || KUNCI_AI.some(([k]) => form.ai[k] !== awal.ai[k]);
    if (aiBerubah) {
      out.ai = {
        aktif: form.aiAktif,
        min_keyakinan: Number(form.ai.min_keyakinan || 0),
        tol_kurang: Number(form.ai.tol_kurang || 0),
        tol_lebih: Number(form.ai.tol_lebih || 0),
        digit_rek: Number(form.ai.digit_rek || 0),
        mundur: Number(form.ai.mundur || 0),
      };
    }
    return out;
  }

  async function simpan() {
    const values = changes();
    if (Object.keys(values).length === 0) {
      setSukses(null);
      setGalatForm("Belum ada perubahan untuk disimpan.");
      return;
    }
    setSibuk(true);
    setGalatForm(null);
    setSukses(null);
    setErrorFields({});
    try {
      await api.put("/admin/settings", { values });
      setSukses("Pengaturan tersimpan.");
      await muat();
    } catch (err) {
      setGalatForm(pesanGalat(err));
      if (err instanceof ApiClientError) setErrorFields(err.mapFields);
    } finally {
      setSibuk(false);
    }
  }

  return (
    <>
      <PageHead title="Pengaturan" sub="Konstanta sistem (Superadmin)" />
      <div className={styles.stack}>
        <TetapanBackup />
        <Card>
          <CardHead title="Nilai tersimpan" />
          {memuat ? (
            <SkeletonList rows={3} />
          ) : galat ? (
            <Notice tone="danger">{galat}</Notice>
          ) : !nilai || Object.keys(nilai).length === 0 ? (
            <EmptyState icon="gear-six">Belum ada pengaturan tersimpan.</EmptyState>
          ) : (
            <Table caption="Nilai pengaturan">
              <thead>
                <tr>
                  <th scope="col">Kunci</th>
                  <th scope="col">Nilai</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(nilai).map(([k, v]) => (
                  <tr key={k}>
                    <td>{k}</td>
                    <td className={`tabular muted ${styles.nilaiSel}`}>
                      {typeof v === "object" ? JSON.stringify(v) : String(v)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Card>

        {form ? (
          <Card>
            <CardHead title="Ubah pengaturan" />
            <Form>
              {galatForm ? <Notice tone="danger">{galatForm}</Notice> : null}
              {sukses ? <Notice tone="success">{sukses}</Notice> : null}

              {KUNCI_ANGKA.map((k) => (
                <Field key={k} label={label(k)} htmlFor={`set-${k}`} error={errorFields[k]}>
                  <Input
                    id={`set-${k}`}
                    type="number"
                    min={0}
                    value={form.angka[k] ?? ""}
                    onChange={(e) =>
                      setForm({ ...form, angka: { ...form.angka, [k]: e.target.value } })
                    }
                  />
                </Field>
              ))}

              <SwitchRow
                checked={form.managerRevisi}
                onChange={(v) => setForm({ ...form, managerRevisi: v })}
                label="Manager boleh merevisi invoice"
                hint="Kunci manager_boleh_revisi"
              />

              <SwitchRow
                checked={form.aiAktif}
                onChange={(v) => setForm({ ...form, aiAktif: v })}
                label="Verifikasi bukti transfer otomatis (AI)"
                hint="Kunci ai.aktif"
              />

              {KUNCI_AI.map(([k, judul, step]) => (
                <Field key={k} label={judul} htmlFor={`set-ai-${k}`} error={errorFields[k]}>
                  <Input
                    id={`set-ai-${k}`}
                    type="number"
                    step={step}
                    min={0}
                    disabled={!form.aiAktif}
                    value={form.ai[k] ?? ""}
                    onChange={(e) =>
                      setForm({ ...form, ai: { ...form.ai, [k]: e.target.value } })
                    }
                  />
                </Field>
              ))}

              <div className={styles.row}>
                <Button variant="primary" disabled={sibuk} onClick={() => void simpan()}>
                  Simpan pengaturan
                </Button>
                <Button
                  variant="secondary"
                  disabled={sibuk || !awal}
                  onClick={() => {
                    if (awal) setForm(awal);
                    setErrorFields({});
                    setGalatForm(null);
                    setSukses(null);
                  }}
                >
                  Kembalikan
                </Button>
              </div>
              <p className="muted small">
                Perubahan biaya packaging berlaku untuk pesanan baru; pesanan lama menyimpan harga
                saat submit.
              </p>
            </Form>
          </Card>
        ) : null}
      </div>
    </>
  );
}
