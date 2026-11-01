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
  Textarea,
} from "@grahita/ui";

import { PenjagaSuperadmin } from "./shell-pages";
import styles from "./admin-pages.module.css";

/** Nilai adalah peta kunci pengaturan dari API (PRD §4.9). */
export type NilaiPengaturan = Record<string, unknown>;

interface OCRStatus {
  ai_active: boolean;
  provider_ready: boolean;
  provider?: string;
  model?: string;
  key_configured?: boolean;
  key_verified?: boolean;
  key_updated_at?: string;
}

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

interface TopikTutorial {
  id?: string;
  ikon: string;
  judul: string;
  ringkas: string;
  langkah: string[];
}

interface TutorialResponse {
  topik: TopikTutorial[];
}

/**
 * TetapanTutorial menyediakan permukaan tulis yang selama ini hanya tersedia
 * di API. Langkah diedit satu per baris agar payload tetap berupa array teks;
 * server tetap menjadi batas sanitasi dan validasi terakhir.
 */
function TetapanTutorial() {
  const [topik, setTopik] = useState<TopikTutorial[]>([]);
  const [memuat, setMemuat] = useState(true);
  const [sibuk, setSibuk] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const [sukses, setSukses] = useState<string | null>(null);

  useEffect(() => {
    let hidup = true;
    void api.get<TutorialResponse>("/admin/tutorial")
      .then((hasil) => { if (hidup) setTopik(hasil.topik); })
      .catch((err: unknown) => { if (hidup) setGalat(pesanGalat(err)); })
      .finally(() => { if (hidup) setMemuat(false); });
    return () => { hidup = false; };
  }, []);

  function ubah(index: number, perubahan: Partial<TopikTutorial>) {
    setTopik((lama) => lama.map((item, i) => i === index ? { ...item, ...perubahan } : item));
    setSukses(null);
  }

  async function simpanTutorial() {
    setSibuk(true);
    setGalat(null);
    setSukses(null);
    try {
      const hasil = await api.put<TutorialResponse>("/admin/tutorial", { topik });
      setTopik(hasil.topik);
      setSukses("Tutorial tersimpan dan langsung tersedia di portal cabang.");
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk(false);
    }
  }

  return (
    <Card>
      <CardHead title="Tutorial portal cabang" />
      <p className="muted small">Topik di bawah muncul sebagai “Panduan tambahan dari Pusat”. Panduan utama bertahap dan tangkapan layar pada portal cabang mengikuti rilis aplikasi.</p>
      {memuat ? <SkeletonList rows={4} /> : (
        <Form>
          {galat ? <Notice tone="danger">{galat}</Notice> : null}
          {sukses ? <Notice tone="success">{sukses}</Notice> : null}
          {topik.length === 0 ? <EmptyState icon="book-open">Belum ada topik tutorial.</EmptyState> : topik.map((item, index) => (
            <Card key={item.id ?? `tutorial-${index}`}>
              <CardHead title={`Topik ${index + 1}`} />
              <Field label="Ikon" htmlFor={`tutorial-${index}-ikon`}>
                <Input id={`tutorial-${index}-ikon`} value={item.ikon}
                  onChange={(e) => ubah(index, { ikon: e.target.value })} />
              </Field>
              <Field label="Judul" htmlFor={`tutorial-${index}-judul`}>
                <Input id={`tutorial-${index}-judul`} value={item.judul}
                  onChange={(e) => ubah(index, { judul: e.target.value })} />
              </Field>
              <Field label="Ringkasan" htmlFor={`tutorial-${index}-ringkas`}>
                <Textarea id={`tutorial-${index}-ringkas`} rows={2} value={item.ringkas}
                  onChange={(e) => ubah(index, { ringkas: e.target.value })} />
              </Field>
              <Field label="Langkah (satu per baris)" htmlFor={`tutorial-${index}-langkah`}>
                <Textarea id={`tutorial-${index}-langkah`} rows={5} value={item.langkah.join("\n")}
                  onChange={(e) => ubah(index, { langkah: e.target.value.split("\n") })} />
              </Field>
            </Card>
          ))}
          <Button variant="primary" disabled={sibuk || memuat || topik.length === 0}
            onClick={() => void simpanTutorial()}>
            {sibuk ? "Menyimpan…" : "Simpan tutorial"}
          </Button>
        </Form>
      )}
    </Card>
  );
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
  konfigurasi_api_siap: boolean;
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
                "Cadangan aplikasi memerlukan tindakan operator."}
            </Notice>
          ) : null}
          <p className="muted small">
            Konfigurasi API: {ringkasan.konfigurasi_api_siap ? "siap" : "belum lengkap"} · Tujuan:{" "}
            {ringkasan.tujuan || "belum dikonfigurasi pada API"} · Target jadwal: {ringkasan.jadwal} · Retensi:{" "}
            {ringkasan.retensi.harian} harian, {ringkasan.retensi.bulanan} bulanan, WAL{" "}
            {ringkasan.retensi.wal_hari} hari
            {ringkasan.retensi.bukti_tanpa_batas ? ", bukti transfer tanpa batas" : ""}.
          </p>
          {ringkasan.terakhir.length === 0 ? (
            <EmptyState icon="cloud-arrow-up">
              Belum ada pekerjaan cadangan aplikasi tercatat. Periksa konfigurasi pada API dan
              worker, lalu jalankan cadangan awal.
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

/** Kunci tidak pernah dibaca balik dari API; status hanya memberi penanda. */
function TetapanGemini({ status, refresh }: { status: OCRStatus; refresh: () => Promise<void> }) {
  const [key, setKey] = useState("");
  const [terlihat, setTerlihat] = useState(false);
  const [sibuk, setSibuk] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const [sukses, setSukses] = useState<string | null>(null);

  async function jalankan(aksi: "simpan" | "uji" | "hapus") {
    setSibuk(true);
    setGalat(null);
    setSukses(null);
    try {
      if (aksi === "simpan") {
        await api.put("/admin/settings/ocr-key", { api_key: key.trim() });
        setKey("");
        setTerlihat(false);
        setSukses("API key tersimpan terenkripsi. Uji koneksi sebelum mengaktifkan AI.");
      } else if (aksi === "uji") {
        await api.post("/admin/settings/ocr-key/test");
        setSukses("Uji gambar dan JSON Gemini berhasil. Uji bukti transfer nyata di QA sebelum mengaktifkan AI.");
      } else {
        await api.delete("/admin/settings/ocr-key");
        setKey("");
        setSukses("API key dihapus.");
      }
      await refresh();
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk(false);
    }
  }

  return (
    <Card>
      <CardHead title="Koneksi AI untuk bukti pembayaran" />
      <p className="muted small">
        Provider: Gemini · Model: {status.model || "gemini-2.5-flash-lite"}. API key hanya dapat
        diisi Superadmin dan tidak ditampilkan kembali setelah disimpan.
      </p>
      <p className="small">
        Status: {status.key_verified ? "Kunci teruji" : status.key_configured ? "Kunci tersimpan, belum teruji" : "Belum ada kunci"}
        {status.key_updated_at ? ` · Diperbarui ${waktuWIB(status.key_updated_at)}` : ""}
      </p>
      {galat ? <Notice tone="danger">{galat}</Notice> : null}
      {sukses ? <Notice tone="success">{sukses}</Notice> : null}
      <Field
        label={status.key_configured ? "Ganti API key Gemini" : "API key Gemini"}
        htmlFor="gemini-api-key"
        hint="Kunci baru menggantikan kunci lama. Matikan AI sebelum mengganti kunci."
      >
        <Input
          id="gemini-api-key"
          type={terlihat ? "text" : "password"}
          autoComplete="new-password"
          spellCheck={false}
          value={key}
          onChange={(e) => setKey(e.target.value)}
        />
      </Field>
      <div className={styles.row}>
        <Button variant="secondary" onClick={() => setTerlihat(!terlihat)} disabled={!key || sibuk}>
          {terlihat ? "Sembunyikan kunci" : "Lihat kunci"}
        </Button>
        <Button variant="primary" onClick={() => void jalankan("simpan")} disabled={!key.trim() || sibuk || status.ai_active}>
          Simpan API key
        </Button>
        <Button variant="secondary" onClick={() => void jalankan("uji")} disabled={!status.key_configured || sibuk}>
          Uji koneksi
        </Button>
        {status.key_configured ? (
          <Button variant="secondary" onClick={() => void jalankan("hapus")} disabled={sibuk || status.ai_active}>
            Hapus API key
          </Button>
        ) : null}
      </div>
    </Card>
  );
}

function PengaturanContent() {
  const [nilai, setNilai] = useState<NilaiPengaturan | null>(null);
  const [ocrStatus, setOcrStatus] = useState<OCRStatus | null>(null);
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
      const [data, status] = await Promise.all([
        api.get<NilaiPengaturan>("/admin/settings"),
        api.get<OCRStatus>("/admin/settings/ocr-status").catch(() => null),
      ]);
      setNilai(data);
      setOcrStatus(status);
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

  const muatStatusOCR = useCallback(async () => {
    setOcrStatus(await api.get<OCRStatus>("/admin/settings/ocr-status"));
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
        {ocrStatus?.provider === "gemini" ? <TetapanGemini status={ocrStatus} refresh={muatStatusOCR} /> : null}
        <TetapanTutorial />
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
                label="Verifikasi otomatis dengan AI"
                hint="Nonaktif: bukti pembayaran pesanan diperiksa admin. Aktif: AI hanya menyetujui pesanan bila semua syarat cocok; sisanya masuk antrean admin. Top-up Deposit dan pelunasan Tempo tetap diverifikasi admin."
                disabled={!form.aiAktif && !ocrStatus?.provider_ready}
              />

              {!ocrStatus?.provider_ready ? (
                <Notice tone="warning">
                  {ocrStatus
                    ? ocrStatus.provider === "gemini"
                      ? "AI belum tersedia. Simpan API key Gemini di atas, lalu uji koneksi sebelum mengaktifkan AI. Verifikasi manual tetap berjalan."
                      : "AI belum tersedia karena provider belum dikonfigurasi pada API. Verifikasi manual tetap berjalan. Minta operator menyiapkan provider pada API dan worker, lalu uji pembacaan bukti sebelum mengaktifkan AI."
                    : "Status provider AI belum dapat diperiksa. Verifikasi manual tetap berjalan; pengaktifan AI ditahan sementara."}
                </Notice>
              ) : form.aiAktif ? (
                <Notice tone="warning">
                  Konfigurasi provider tersedia pada API. Pastikan worker memiliki konfigurasi yang sama dan pembacaan bukti nyata sudah diuji.
                </Notice>
              ) : null}

              {form.aiAktif && KUNCI_AI.map(([k, judul, step]) => (
                <Field key={k} label={judul} htmlFor={`set-ai-${k}`} error={errorFields[k]}>
                  <Input
                    id={`set-ai-${k}`}
                    type="number"
                    step={step}
                    min={0}
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
