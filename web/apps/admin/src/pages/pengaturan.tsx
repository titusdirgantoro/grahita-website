import { useCallback, useEffect, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import {
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
    cnc_hari: "Tenggat Cash & Carry (hari)",
    token_hari: "Masa berlaku token registrasi (hari)",
    reset_menit: "Masa berlaku tautan reset (menit)",
    max_mb: "Ukuran maksimal bukti transfer (MB)",
    max_file: "Jumlah maksimal file bukti",
    digit_invoice: "Jumlah digit urut nomor invoice",
  })[kunci] ?? kunci;

/** Kunci bilangan bulat yang ditampilkan sebagai input angka. */
const KUNCI_ANGKA = [
  "biaya_packaging",
  "cnc_hari",
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
 * (PRD §2.1, §4.9). Semua nilai divalidasi server (tipe + rentang); pesan
 * kesalahan per kunci ditampilkan pada field yang bersangkutan. Perubahan
 * biaya packaging tidak memengaruhi pesanan lama karena harga di-snapshot
 * pada order line saat submit (BR-05, CP-SER-05).
 */
export function PengaturanPage() {
  return (
    <PenjagaSuperadmin>
      <PengaturanContent />
    </PenjagaSuperadmin>
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
                    <td className="tabular muted">
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
