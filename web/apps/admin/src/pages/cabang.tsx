import { useCallback, useEffect, useMemo, useState } from "react";

import { useNavigate, useSearch } from "@tanstack/react-router";

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
  Modal,
  Notice,
  PageHead,
  Segmented,
  SkeletonList,
  SwitchRow,
  Table,
  useToast,
  type BadgeTone,
} from "@grahita/ui";

import { formatWaktu } from "./pengguna";
import type { Cabang } from "./shell-pages";
import { useSesi } from "../session";
import styles from "./admin-pages.module.css";

/**
 * AkunCabang adalah satu baris GET /admin/users/branch (F1-05).
 *
 * Dipakai kolom "Akun" dan blok token: /admin/branches tidak mengembalikan
 * apakah cabang sudah punya akun (F1-06 memutuskan itu di service lewat
 * `branches.HasAccount`), sedangkan endpoint user management cabang
 * mengembalikannya per cabang — `status` = aktif | belum_registrasi | nonaktif.
 */
interface AkunCabang {
  branch_id: string;
  has_account: boolean;
  status: "aktif" | "belum_registrasi" | "nonaktif";
}

/** TokenRegistrasi adalah hasil POST /admin/branches/{id}/token (PRD §4.4). */
interface TokenRegistrasi {
  token: string;
  prefix: string;
  expires_at: string;
}

interface RekeningAssignment {
  id: string;
  bank: string;
  nama_pemilik: string;
  display_nomor: string;
  is_active: boolean;
  is_allowed: boolean;
  dormant: boolean;
  effective: boolean;
}

interface AssignmentCabang {
  branch_id: string;
  version: number;
  items: RekeningAssignment[];
}

/** TabCabang adalah tab filter daftar (prototype `S.cabF`). */
type TabCabang = "semua" | "cnc" | "special" | "nonaktif";

const TAB: ReadonlyArray<{ value: TabCabang; label: string }> = [
  { value: "semua", label: "Semua" },
  { value: "cnc", label: "C&C" },
  { value: "special", label: "Special" },
  { value: "nonaktif", label: "Nonaktif" },
];

const TONE_AKUN: Record<AkunCabang["status"], BadgeTone> = {
  aktif: "success",
  belum_registrasi: "draft",
  nonaktif: "neutral",
};

const LABEL_AKUN: Record<AkunCabang["status"], string> = {
  aktif: "Aktif",
  belum_registrasi: "Belum registrasi",
  nonaktif: "Nonaktif",
};

interface FormCabang {
  /** Kosong berarti cabang baru. */
  id?: string;
  nama: string;
  nama_kepala: string;
  jabatan_kepala: string;
  email: string;
  identity_status: "identity_incomplete" | "ready" | "registered";
  telepon: string;
  is_cnc: boolean;
  is_special: boolean;
  pajak_in: boolean;
  is_active: boolean;
  /** Email saat dialog dibuka: dasar konfirmasi "Ya, ubah email login". */
  emailAwal: string;
  /** Cabang sudah punya akun sehingga emailnya adalah email login akun itu. */
  punyaAkun: boolean;
  /** Prefix token aktif (nilai aslinya tidak bisa dibaca lagi dari server). */
  token_prefix?: string;
  token_expires_at?: string;
}

const CABANG_BARU: FormCabang = {
  nama: "",
  nama_kepala: "",
  jabatan_kepala: "",
  email: "",
  identity_status: "identity_incomplete",
  telepon: "",
  is_cnc: false,
  is_special: false,
  pajak_in: false,
  is_active: true,
  emailAwal: "",
  punyaAkun: false,
};

const POLA_EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function pesanGalat(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Terjadi kesalahan. Coba lagi.";
}

/**
 * validasiForm memeriksa bentuk data sebelum dikirim. Server tetap otoritas
 * akhir (unik email, aturan domain); ini hanya mempercepat umpan balik.
 */
function validasiForm(f: FormCabang): Record<string, string> {
  const err: Record<string, string> = {};
  if (f.nama.trim().length < 2) err.nama = "Nama cabang wajib diisi (minimal 2 karakter).";
  if (f.nama_kepala.trim() && f.nama_kepala.trim().length < 2) {
    err.nama_kepala = "Nama kepala cabang minimal 2 karakter bila diisi.";
  }
  if (f.email.trim() && !POLA_EMAIL.test(f.email.trim())) err.email = "Format email belum benar.";
  return err;
}

/**
 * CabangPage — /cabang: master data cabang (PRD §4.4 AP-CAB-01..05).
 *
 * Keputusan yang tampak di UI, semuanya ditegakkan server:
 *  - filter tab dijalankan SERVER lewat `?q=&cnc=&special=&aktif=` (F1-06
 *    menyediakan filternya, jadi daftar tidak perlu dimuat seluruhnya),
 *  - satu akun per cabang: blok "Akun & token registrasi" hanya menawarkan
 *    token untuk cabang yang BELUM punya akun; cabang nonaktif tidak boleh
 *    membuat token (aktifkan dulu),
 *  - nilai asli token hanya tampil sekali (respons generate) dengan tombol
 *    salin; generate berikutnya membatalkan token lama,
 *  - hapus selalu dikonfirmasi, dan 409 BRANCH_HAS_ORDERS dialihkan ke jalur
 *    "nonaktifkan" (AP-CAB-04) bukan diulang-ulang,
 *  - mengubah email cabang yang sudah punya akun berarti memindahkan email
 *    login, jadi diminta konfirmasi lebih dulu (seperti prototype).
 */
export function CabangPage() {
  const { tampilkan } = useToast();
  const { peran } = useSesi();
  const [tab, setTab] = useState<TabCabang>("semua");
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<Cabang[] | null>(null);
  const [akun, setAkun] = useState<Map<string, AkunCabang> | null>(null);
  const [memuat, setMemuat] = useState(true);
  const [galat, setGalat] = useState<string | null>(null);
  const [galatAkun, setGalatAkun] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState("");

  const [form, setForm] = useState<FormCabang | null>(null);
  const [galatForm, setGalatForm] = useState<string | null>(null);
  const [errorFields, setErrorFields] = useState<Record<string, string>>({});
  const [konfirmasiEmail, setKonfirmasiEmail] = useState(false);
  const [tokenBaru, setTokenBaru] = useState<TokenRegistrasi | null>(null);

  const [hapus, setHapus] = useState<Cabang | null>(null);
  const [galatHapus, setGalatHapus] = useState<{ pesan: string; bolehNonaktif: boolean } | null>(
    null,
  );
  const [rekeningCabang, setRekeningCabang] = useState<Cabang | null>(null);
  const [assignment, setAssignment] = useState<AssignmentCabang | null>(null);
  const [pilihanRekening, setPilihanRekening] = useState<Set<string>>(new Set());
  const [galatRekening, setGalatRekening] = useState<string | null>(null);
  const [memuatRekening, setMemuatRekening] = useState(false);

  const query = useMemo(() => {
    const p = new URLSearchParams();
    if (q.trim()) p.set("q", q.trim());
    if (tab === "cnc") p.set("cnc", "true");
    if (tab === "special") p.set("special", "true");
    if (tab === "nonaktif") p.set("aktif", "false");
    const s = p.toString();
    return s ? `?${s}` : "";
  }, [q, tab]);

  /**
   * muat memuat master cabang + peta akun cabang.
   *
   * Dua permintaan terpisah: kegagalan daftar akun tidak boleh menyembunyikan
   * master cabang. Tanpa peta akun, blok token berhenti menebak dan hanya
   * mengandalkan jawaban server (409 BRANCH_HAS_ACCOUNT).
   */
  const muat = useCallback(async () => {
    setMemuat(true);
    try {
      const [daftar, akunCabang] = await Promise.all([
        api.get<Cabang[]>(`/admin/branches${query}`),
        api.get<AkunCabang[]>("/admin/users/branch").catch((err: unknown) => {
          setGalatAkun(pesanGalat(err));
          return null;
        }),
      ]);
      setRows(daftar);
      if (akunCabang) {
        setAkun(new Map(akunCabang.map((a) => [a.branch_id, a])));
        setGalatAkun(null);
      }
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, [query]);

  useEffect(() => {
    void muat();
  }, [muat]);

  function bukaTambah() {
    setGalatForm(null);
    setErrorFields({});
    setKonfirmasiEmail(false);
    setTokenBaru(null);
    setForm({ ...CABANG_BARU });
  }

  // Tombol "+" di topbar membuka halaman ini dengan ?tambah=true: buka form
  // sekali, lalu bersihkan parameternya supaya muat ulang tidak membukanya lagi.
  const { tambah } = useSearch({ strict: false }) as { tambah?: boolean };
  const navigate = useNavigate();
  useEffect(() => {
    if (!tambah) return;
    bukaTambah();
    void navigate({ to: "/cabang", search: {}, replace: true });
  }, [tambah]);

  function bukaUbah(row: Cabang) {
    setGalatForm(null);
    setErrorFields({});
    setKonfirmasiEmail(false);
    setTokenBaru(null);
    setForm({
      id: row.id,
      nama: row.nama,
      nama_kepala: row.nama_kepala,
      jabatan_kepala: row.jabatan_kepala ?? "",
      email: row.email,
      identity_status: row.identity_status ?? (row.email && row.nama_kepala ? "ready" : "identity_incomplete"),
      telepon: row.telepon ?? "",
      is_cnc: row.is_cnc,
      is_special: Boolean(row.is_special),
      pajak_in: Boolean(row.pajak_in),
      is_active: row.is_active,
      emailAwal: row.email,
      punyaAkun: Boolean(akun?.get(row.id)?.has_account),
      token_prefix: row.token_prefix,
      token_expires_at: row.token_expires_at,
    });
  }

  /** terapkanGalatForm menempelkan pesan server ke field yang tepat. */
  function terapkanGalatForm(err: unknown) {
    const pesan = pesanGalat(err);
    if (err instanceof ApiClientError) {
      const fields = { ...err.mapFields };
      // EMAIL_TAKEN dikirim tanpa `fields`; tampilkan di input email (PRD §4.4).
      if (err.code === "EMAIL_TAKEN") fields.email = err.message;
      if (Object.keys(fields).length > 0) {
        setErrorFields(fields);
        setGalatForm(null);
        return;
      }
    }
    setErrorFields({});
    setGalatForm(pesan);
  }

  async function simpan() {
    if (!form) return;
    const salah = validasiForm(form);
    setErrorFields(salah);
    if (Object.keys(salah).length > 0) {
      setGalatForm(null);
      setKonfirmasiEmail(false);
      return;
    }
    const emailBerubah = form.email.trim().toLowerCase() !== form.emailAwal.trim().toLowerCase();
    if (form.id && form.punyaAkun && emailBerubah && !konfirmasiEmail) {
      setKonfirmasiEmail(true);
      return;
    }

    setSibuk("form");
    setGalatForm(null);
    const muatan = {
      nama: form.nama.trim(),
      nama_kepala: form.nama_kepala.trim(),
      jabatan_kepala: form.jabatan_kepala.trim(),
      email: form.email.trim(),
      telepon: form.telepon.trim(),
      is_cnc: form.is_cnc,
      is_special: form.is_special,
      pajak_in: form.pajak_in,
      is_active: form.is_active,
    };
    try {
      if (form.id) {
        await api.put(`/admin/branches/${form.id}`, muatan);
        tampilkan(`Cabang ${muatan.nama} disimpan`);
      } else {
        await api.post("/admin/branches", muatan);
        tampilkan(`Cabang ${muatan.nama} ditambahkan. Buat token registrasi dari detail cabang.`);
      }
      setForm(null);
      setKonfirmasiEmail(false);
      await muat();
    } catch (err) {
      terapkanGalatForm(err);
    } finally {
      setSibuk("");
    }
  }

  async function buatToken() {
    if (!form?.id) return;
    setSibuk("token");
    setGalatForm(null);
    try {
      const hasil = await api.post<TokenRegistrasi>(`/admin/branches/${form.id}/token`);
      setTokenBaru(hasil);
      setForm((lama) =>
        lama ? { ...lama, token_prefix: hasil.prefix, token_expires_at: hasil.expires_at } : lama,
      );
      tampilkan("Token registrasi dibuat. Token lama tidak berlaku lagi.");
      await muat();
    } catch (err) {
      setGalatForm(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  async function salinToken(teks: string) {
    const klip = navigator.clipboard;
    if (!klip) {
      tampilkan("Papan klip tidak tersedia; salin token secara manual.");
      return;
    }
    try {
      await klip.writeText(teks);
      tampilkan("Token disalin ke papan klip.");
    } catch {
      tampilkan("Gagal menyalin otomatis; salin token secara manual.");
    }
  }

  async function ubahAktif(row: Cabang, aktif: boolean, kunci: string) {
    setSibuk(kunci);
    try {
      const hasil = await api.put<Cabang>(`/admin/branches/${row.id}`, { is_active: aktif });
      tampilkan(`Cabang ${hasil.nama} ${hasil.is_active ? "diaktifkan" : "dinonaktifkan"}`);
      setHapus(null);
      setGalatHapus(null);
      await muat();
    } catch (err) {
      if (kunci === "hapus") {
        setGalatHapus({ pesan: pesanGalat(err), bolehNonaktif: false });
      } else {
        setGalat(pesanGalat(err));
      }
    } finally {
      setSibuk("");
    }
  }

  function mintaHapus(row: Cabang) {
    setGalatHapus(null);
    setHapus(row);
  }

  async function konfirmasiHapus() {
    if (!hapus) return;
    setSibuk("hapus");
    try {
      await api.delete(`/admin/branches/${hapus.id}`);
      tampilkan(`Cabang ${hapus.nama} dihapus`);
      setHapus(null);
      setGalatHapus(null);
      await muat();
    } catch (err) {
      const kode = err instanceof ApiClientError ? err.code : "";
      setGalatHapus({ pesan: pesanGalat(err), bolehNonaktif: kode === "BRANCH_HAS_ORDERS" });
    } finally {
      setSibuk("");
    }
  }

  async function muatAssignment(cabang: Cabang) {
    setRekeningCabang(cabang);
    setAssignment(null);
    setGalatRekening(null);
    setMemuatRekening(true);
    try {
      const hasil = await api.get<AssignmentCabang>(`/admin/branches/${cabang.id}/bank-accounts`);
      setAssignment(hasil);
      setPilihanRekening(new Set(hasil.items.filter((a) => a.is_allowed).map((a) => a.id)));
    } catch (err) {
      setGalatRekening(pesanGalat(err));
    } finally {
      setMemuatRekening(false);
    }
  }

  function ubahPilihanRekening(id: string, checked: boolean) {
    setPilihanRekening((lama) => {
      const baru = new Set(lama);
      if (checked) baru.add(id);
      else baru.delete(id);
      return baru;
    });
  }

  function cabutAssignmentDormant(rekening: RekeningAssignment) {
    const lanjut = window.confirm(
      `${rekening.bank} ${rekening.display_nomor} sedang nonaktif. Setelah dicabut, rekening ini tidak dapat dipilih kembali sampai diaktifkan. Cabut assignment?`,
    );
    if (lanjut) ubahPilihanRekening(rekening.id, false);
  }

  async function simpanAssignment() {
    if (!rekeningCabang || !assignment) return;
    if (pilihanRekening.size === 0 && !window.confirm("Tanpa rekening, transfer cabang ini akan diblokir. Lanjutkan?")) return;
    setSibuk("rekening");
    setGalatRekening(null);
    try {
      await api.put(`/admin/branches/${rekeningCabang.id}/bank-accounts`, {
        expected_version: assignment.version,
        bank_account_ids: Array.from(pilihanRekening),
      });
      tampilkan("Assignment rekening tujuan disimpan.");
      setRekeningCabang(null);
      setAssignment(null);
      await muat();
    } catch (err) {
      if (err instanceof ApiClientError && err.code === "ALLOWLIST_VERSION_CONFLICT") {
        setGalatRekening("Data telah berubah. Daftar dimuat ulang; periksa pilihan sebelum menyimpan lagi.");
        const terbaru = await api.get<AssignmentCabang>(`/admin/branches/${rekeningCabang.id}/bank-accounts`);
        setAssignment(terbaru);
        setPilihanRekening(new Set(terbaru.items.filter((a) => a.is_allowed).map((a) => a.id)));
      } else {
        setGalatRekening(pesanGalat(err));
      }
    } finally {
      setSibuk("");
    }
  }

  const baris = rows ?? [];
  const jumlahAkunAktif = akun
    ? Array.from(akun.values()).filter((a) => a.status === "aktif").length
    : null;
  const terfilter = tab !== "semua" || q.trim() !== "";
  const sub =
    rows === null
      ? "Master data cabang"
      : terfilter
        ? `${baris.length} cabang cocok dengan filter`
        : `${baris.length} cabang${jumlahAkunAktif === null ? "" : ` · ${jumlahAkunAktif} akun aktif`}`;

  return (
    <>
      <PageHead
        title="Cabang"
        sub={sub}
        actions={
          <Button variant="primary" icon="plus" onClick={bukaTambah}>
            Tambah cabang
          </Button>
        }
      />
      <Card>
        <CardHead
          title="Daftar cabang"
          tools={
            <div className={styles.row}>
              <Segmented label="Filter cabang" options={TAB} value={tab} onChange={setTab} />
              <Input
                aria-label="Cari cabang"
                placeholder="Cari nama atau email"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </div>
          }
        />
        {galat ? (
          <div style={{ marginBottom: 12 }}>
            <Notice tone="danger">{galat}</Notice>
          </div>
        ) : null}
        {galatAkun ? (
          <div style={{ marginBottom: 12 }}>
            <Notice tone="warning">
              Status akun cabang tidak bisa dimuat: {galatAkun} Blok token tetap memakai jawaban
              server saat token dibuat.
            </Notice>
          </div>
        ) : null}
        {memuat ? (
          <SkeletonList rows={3} />
        ) : baris.length === 0 ? (
          <EmptyState icon="storefront">
            {terfilter ? "Tidak ada cabang yang cocok." : "Belum ada cabang terdaftar."}
          </EmptyState>
        ) : (
          <Table stack="wide" caption="Master data cabang">
            <thead>
              <tr>
                <th scope="col">Nama cabang</th>
                <th scope="col">Kepala cabang</th>
                <th scope="col">Flag</th>
                <th scope="col">Akun</th>
                <th scope="col">Rekening pembayaran</th>
                <th scope="col">Status</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {baris.map((c) => {
                const akunRow = akun?.get(c.id);
                return (
                  <tr key={c.id}>
                    <td>
                      <b>{c.nama}</b>
                      <div className={`muted small ${styles.emailSel}`}>{c.email || "—"}</div>
                    </td>
                    <td>{c.nama_kepala || "—"}</td>
                    <td>
                      <div className={styles.row}>
                        {c.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
                        {c.is_special ? <Badge tone="info">Special</Badge> : null}
                        {c.pajak_in ? <Badge tone="neutral">PI</Badge> : null}
                      </div>
                    </td>
                    <td>
                      {akunRow ? (
                        <Badge tone={TONE_AKUN[akunRow.status]}>{LABEL_AKUN[akunRow.status]}</Badge>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td>
                      {(c.effective_bank_account_count ?? 0) > 0 ? (
                        <Badge tone="success">{`${c.effective_bank_account_count} aktif`}</Badge>
                      ) : (
                        <Badge tone="danger">Diblokir—belum ada rekening</Badge>
                      )}
                    </td>
                    <td>
                      <Badge tone={!c.is_active ? "danger" : c.identity_status === "identity_incomplete" ? "warning" : "success"}>
                        {!c.is_active ? "Nonaktif" : c.identity_status === "identity_incomplete" ? "Identitas belum lengkap" : "Aktif"}
                      </Badge>
                    </td>
                    <td className={`num ${styles.aksiSel}`}>
                      <div className={`${styles.aksi} ${styles.aksiIkon}`}>
                        <Button variant="ghost" icon="pencil-simple" title="Ubah" onClick={() => bukaUbah(c)}>
                          <span className={styles.aksiTeks}>Ubah</span>
                        </Button>
                        {peran === "superadmin" ? (
                          <Button
                            variant="secondary"
                            icon="bank"
                            title="Kelola rekening tujuan"
                            onClick={() => void muatAssignment(c)}
                          >
                            <span className={styles.aksiTeks}>Kelola rekening tujuan</span>
                          </Button>
                        ) : null}
                        <Button
                          variant="secondary"
                          icon={c.is_active ? "prohibit" : "check-circle"}
                          title={c.is_active ? "Nonaktifkan" : "Aktifkan"}
                          disabled={sibuk === c.id}
                          onClick={() => void ubahAktif(c, !c.is_active, c.id)}
                        >
                          <span className={styles.aksiTeks}>{c.is_active ? "Nonaktifkan" : "Aktifkan"}</span>
                        </Button>
                        <Button
                          variant="danger-ghost"
                          icon="trash"
                          title="Hapus"
                          disabled={sibuk === c.id}
                          onClick={() => mintaHapus(c)}
                        >
                          <span className={styles.aksiTeks}>Hapus</span>
                        </Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        )}
        <p className="muted small" style={{ marginTop: 12 }}>
          Cabang yang sudah punya pesanan tidak bisa dihapus &mdash; nonaktifkan saja (PRD
          AP-CAB-04). Token registrasi hanya berlaku untuk cabang yang belum punya akun.
        </p>
      </Card>

      <Modal
        open={form !== null}
        title={form?.id ? "Detail cabang" : "Tambah cabang"}
        onClose={() => {
          setForm(null);
          setKonfirmasiEmail(false);
          setTokenBaru(null);
        }}
        wide
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setForm(null);
                setKonfirmasiEmail(false);
                setTokenBaru(null);
              }}
            >
              Batal
            </Button>
            <Button
              variant="primary"
              disabled={sibuk === "form" || !form?.nama.trim()}
              onClick={() => void simpan()}
            >
              {konfirmasiEmail ? "Ya, ubah email login" : "Simpan"}
            </Button>
          </>
        }
      >
        {form ? (
          <Form>
            {galatForm ? <Notice tone="danger">{galatForm}</Notice> : null}
            <Field label="Nama cabang" htmlFor="cf-nama" error={errorFields.nama}>
              <Input
                id="cf-nama"
                value={form.nama}
                onChange={(e) => setForm({ ...form, nama: e.target.value })}
              />
            </Field>
            <Field label="Nama kepala cabang" htmlFor="cf-kepala" error={errorFields.nama_kepala}>
              <Input
                id="cf-kepala"
                value={form.nama_kepala}
                onChange={(e) => setForm({ ...form, nama_kepala: e.target.value })}
              />
            </Field>
            <Field
              label="Jabatan kepala cabang"
              htmlFor="cf-jabatan"
              hint="Opsional."
              error={errorFields.jabatan_kepala}
            >
              <Input
                id="cf-jabatan"
                value={form.jabatan_kepala}
                onChange={(e) => setForm({ ...form, jabatan_kepala: e.target.value })}
              />
            </Field>
            <Field
              label="Email cabang"
              htmlFor="cf-email"
              hint={
                form.punyaAkun
                  ? undefined
                  : "Opsional saat membuat cabang; wajib sebelum token registrasi dapat dibuat."
              }
              error={errorFields.email}
            >
              <Input
                id="cf-email"
                type="email"
                value={form.email}
                onChange={(e) => {
                  setForm({ ...form, email: e.target.value });
                  setKonfirmasiEmail(false);
                }}
              />
            </Field>
            {konfirmasiEmail ? (
              <Notice tone="warning">
                Email login cabang akan berubah dari {form.emailAwal} menjadi {form.email.trim()}.
                Pilih &ldquo;Ya, ubah email login&rdquo; untuk melanjutkan.
              </Notice>
            ) : null}
            <Field
              label="Telepon"
              htmlFor="cf-telepon"
              hint="Opsional, mis. +62812xxxxxxx."
              error={errorFields.telepon}
            >
              <Input
                id="cf-telepon"
                value={form.telepon}
                onChange={(e) => setForm({ ...form, telepon: e.target.value })}
              />
            </Field>
            <SwitchRow
              checked={form.is_cnc}
              onChange={(v) => setForm({ ...form, is_cnc: v })}
              label="Cash and Carry"
              hint="Cabang boleh mengirim pesanan tanpa bukti transfer."
            />
            <SwitchRow
              checked={form.is_special}
              onChange={(v) => setForm({ ...form, is_special: v })}
              label="Special"
              hint="Disimpan sebagai atribut & filter; efeknya belum ditentukan (PRD AP-CAB-05)."
            />
            <SwitchRow
              checked={form.pajak_in}
              onChange={(v) => setForm({ ...form, pajak_in: v })}
              label="Pajak In (PI)"
              hint="Disimpan sebagai atribut & filter; belum mengubah harga atau invoice."
            />
            <SwitchRow
              checked={form.is_active}
              onChange={(v) => setForm({ ...form, is_active: v })}
              label="Cabang aktif"
              hint="Cabang nonaktif tidak bisa masuk dan tidak muncul di pilihan aktif."
            />

            <div className={styles.tokenBox}>
              <div className={styles.row}>
                <b>Akun &amp; token registrasi</b>
                {form.id ? (
                  form.punyaAkun ? (
                    <Badge tone="success">Sudah punya akun</Badge>
                  ) : (
                    <Badge tone="draft">Belum registrasi</Badge>
                  )
                ) : null}
              </div>
              <BlokToken
                form={form}
                tokenBaru={tokenBaru}
                sibuk={sibuk === "token"}
                onBuat={() => void buatToken()}
                onSalin={(teks) => void salinToken(teks)}
              />
            </div>
          </Form>
        ) : null}
      </Modal>

      <Modal
        open={rekeningCabang !== null}
        title={`Rekening tujuan · ${rekeningCabang?.nama ?? ""}`}
        onClose={() => {
          if (sibuk !== "rekening") setRekeningCabang(null);
        }}
        wide
        footer={
          <>
            <Button variant="secondary" onClick={() => setRekeningCabang(null)} disabled={sibuk === "rekening"}>
              Batal
            </Button>
            <Button variant="primary" onClick={() => void simpanAssignment()} disabled={!assignment || sibuk === "rekening"}>
              {sibuk === "rekening" ? "Menyimpan…" : "Simpan assignment"}
            </Button>
          </>
        }
      >
        <Notice tone="warning">
          Hanya rekening aktif yang efektif. Mengosongkan semua pilihan memblokir instruksi transfer dan unggah bukti cabang.
        </Notice>
        {galatRekening ? <Notice tone="danger">{galatRekening}</Notice> : null}
        {memuatRekening ? <SkeletonList rows={3} /> : null}
        {assignment ? (
          <Table caption="Assignment rekening tujuan cabang">
            <thead>
              <tr>
                <th scope="col">Pilih</th>
                <th scope="col">Rekening</th>
                <th scope="col">Pemilik</th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {assignment.items.map((r) => {
                const dormant = r.dormant && r.is_allowed;
                const disabled = !r.is_active && !r.is_allowed;
                return (
                  <tr key={r.id}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`Pilih ${r.bank} ${r.display_nomor}`}
                        checked={pilihanRekening.has(r.id)}
                        disabled={disabled || dormant}
                        onChange={(e) => ubahPilihanRekening(r.id, e.target.checked)}
                      />
                      {dormant ? (
                        <Button variant="danger-ghost" onClick={() => cabutAssignmentDormant(r)}>
                          Cabut assignment
                        </Button>
                      ) : null}
                    </td>
                    <td>{`${r.bank} · ${r.display_nomor}`}</td>
                    <td>{r.nama_pemilik}</td>
                    <td>
                      {dormant ? (
                        <Badge tone="warning">Dormant</Badge>
                      ) : r.effective ? (
                        <Badge tone="success">Efektif</Badge>
                      ) : r.is_active ? (
                        <Badge tone="neutral">Tidak dipilih</Badge>
                      ) : (
                        <Badge tone="neutral">Nonaktif</Badge>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        ) : null}
      </Modal>

      <Modal
        open={hapus !== null}
        title="Hapus cabang"
        onClose={() => {
          setHapus(null);
          setGalatHapus(null);
        }}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setHapus(null);
                setGalatHapus(null);
              }}
            >
              Batal
            </Button>
            {galatHapus?.bolehNonaktif && hapus ? (
              <Button
                variant="primary"
                disabled={sibuk === "hapus"}
                onClick={() => void ubahAktif(hapus, false, "hapus")}
              >
                Nonaktifkan cabang
              </Button>
            ) : (
              <Button variant="primary" disabled={sibuk === "hapus"} onClick={() => void konfirmasiHapus()}>
                Hapus cabang
              </Button>
            )}
          </>
        }
      >
        {hapus ? (
          <Form>
            {galatHapus ? <Notice tone="danger">{galatHapus.pesan}</Notice> : null}
            <p>
              Hapus cabang <b>{hapus.nama}</b> ({hapus.email})? Data cabang beserta akun dan
              tokennya ikut terhapus dan tidak bisa dikembalikan.
            </p>
            <p className="muted small">
              Cabang yang sudah punya pesanan tidak bisa dihapus &mdash; pilih Nonaktifkan cabang
              sebagai gantinya.
            </p>
          </Form>
        ) : null}
      </Modal>
    </>
  );
}

/**
 * BlokToken menampilkan keadaan token registrasi sebuah cabang.
 *
 * Aturan (server tetap otoritas akhir): cabang baru harus disimpan dulu,
 * cabang berakun tidak butuh token (pemulihan lewat lupa kata sandi), cabang
 * nonaktif harus diaktifkan dulu, dan nilai asli token hanya tampil pada
 * respons pembuatan.
 */
function BlokToken({
  form,
  tokenBaru,
  sibuk,
  onBuat,
  onSalin,
}: {
  form: FormCabang;
  tokenBaru: TokenRegistrasi | null;
  sibuk: boolean;
  onBuat: () => void;
  onSalin: (teks: string) => void;
}) {
  if (!form.id) {
    return (
      <p className="muted small">Simpan cabang terlebih dahulu untuk membuat token registrasi.</p>
    );
  }
  if (form.punyaAkun) {
    return (
      <p className="muted small">
        Cabang ini sudah punya akun, jadi token tidak diperlukan. Pemulihan akses lewat lupa kata
        sandi.
      </p>
    );
  }
  if (!form.is_active) {
    return (
      <Notice tone="warning">
        Cabang sedang nonaktif; aktifkan dulu sebelum membuat token registrasi.
      </Notice>
    );
  }
  if (!form.nama_kepala.trim() || !form.email.trim() || form.identity_status === "identity_incomplete") {
    return (
      <Notice tone="warning">
        Lengkapi nama kepala cabang dan email, lalu simpan sebelum membuat token registrasi.
      </Notice>
    );
  }
  return (
    <>
      {tokenBaru ? (
        <Field
          label="Token registrasi"
          htmlFor="cf-token"
          hint={`Berlaku sampai ${formatWaktu(tokenBaru.expires_at)}.`}
        >
          <div className={styles.row}>
            <Input id="cf-token" readOnly value={tokenBaru.token} />
            <Button variant="secondary" icon="copy" onClick={() => onSalin(tokenBaru.token)}>
              Salin
            </Button>
          </div>
        </Field>
      ) : form.token_prefix ? (
        <p className="muted small">
          Token aktif <b>{form.token_prefix}…</b> berlaku sampai {formatWaktu(form.token_expires_at)}.
          Nilai lengkapnya hanya tampil saat token dibuat.
        </p>
      ) : (
        <p className="muted small">Belum ada token aktif.</p>
      )}
      {tokenBaru ? (
        <Notice tone="warning">
          Salin token sekarang &mdash; nilainya tidak bisa ditampilkan lagi setelah dialog ditutup.
        </Notice>
      ) : null}
      <div className={styles.row}>
        <Button variant="secondary" icon="key" disabled={sibuk} onClick={onBuat}>
          {sibuk ? "Membuat token…" : form.token_prefix || tokenBaru ? "Buat token baru" : "Buat token"}
        </Button>
      </div>
    </>
  );
}
