import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
import {
  AuthLayout,
  AuthLinks,
  Button,
  Field,
  Form,
  Input,
  Notice,
  PasswordResetPage,
  PasswordStrength,
  periksaKataSandiBaru,
} from "@grahita/ui";

import { useSesi } from "../session";

/** pMasuk — /masuk (customer_v2.html: email cabang + password). */
export function HalamanMasuk({ terdaftar = false }: { terdaftar?: boolean } = {}) {
  const { masuk } = useSesi();
  const navigate = useNavigate();
  const [galat, setGalat] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState(false);

  async function kirim(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setSibuk(true);
    setGalat(null);
    try {
      await masuk(String(data.get("email") ?? ""), String(data.get("password") ?? ""));
      await navigate({ to: "/" });
    } catch (err) {
      setGalat(err instanceof ApiClientError ? err.message : "Terjadi kesalahan tak terduga.");
    } finally {
      setSibuk(false);
    }
  }

  return (
    <AuthLayout hint="Portal cabang Grahita Indonesia. Hubungi admin Pusat bila tidak bisa masuk.">
      <Form onSubmit={(e) => void kirim(e)}>
        <div>
          <h1>Masuk ke portal cabang</h1>
          <p className="muted">Pesan produk Grahita dan pantau invoice cabang Anda.</p>
        </div>
        {terdaftar ? (
          <Notice tone="success">
            Akun cabang berhasil dibuat. Masuk dengan email cabang dan kata sandi yang baru saja Anda isi.
          </Notice>
        ) : null}
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        <Field label="Email cabang" htmlFor="em">
          <Input id="em" name="email" type="email" autoComplete="username" required />
        </Field>
        <Field label="Password" htmlFor="pw">
          <Input id="pw" name="password" type="password" autoComplete="current-password" required />
        </Field>
        <Button type="submit" variant="primary" block disabled={sibuk}>
          {sibuk ? "Memproses…" : "Masuk"}
        </Button>
        <AuthLinks>
          <Link to="/lupa">Lupa password</Link>
          <Link to="/daftar">Daftar dengan token</Link>
        </AuthLinks>
      </Form>
    </AuthLayout>
  );
}

function FieldBaca({ label, nilai, htmlFor }: { label: string; nilai: string; htmlFor: string }) {
  return (
    <Field label={label} htmlFor={htmlFor}>
      <Input id={htmlFor} value={nilai} readOnly tabIndex={-1} />
    </Field>
  );
}

/** DataCabangRegistrasi adalah isi 200 POST /auth/registration/validate. */
interface DataCabangRegistrasi {
  nama_cabang: string;
  nama_kepala: string;
  email: string;
}

/**
 * PESAN_TOKEN memetakan kode error F1-07 ke pesan spesifik berisi arahan
 * menghubungi admin Pusat (PRD CP-REG-07). Pesan backend tetap dipakai untuk
 * kode di luar daftar ini (mis. VALIDATION_FAILED, UNAVAILABLE).
 */
const PESAN_TOKEN: Record<string, string> = {
  TOKEN_INVALID:
    "Token registrasi tidak dikenali. Periksa kembali penulisannya, atau minta token baru ke admin Pusat.",
  TOKEN_EXPIRED: "Token registrasi sudah kedaluwarsa. Minta token baru ke admin Pusat.",
  TOKEN_USED:
    "Token registrasi sudah dipakai. Satu cabang hanya punya satu akun \u2014 gunakan lupa kata sandi bila perlu masuk kembali.",
  BRANCH_INACTIVE: "Cabang ini sedang nonaktif. Hubungi admin Pusat untuk mengaktifkannya kembali.",
  BRANCH_HAS_ACCOUNT: "Cabang ini sudah punya akun. Gunakan lupa kata sandi bila perlu masuk kembali.",
  EMAIL_TAKEN: "Email cabang sudah dipakai akun lain. Hubungi admin Pusat.",
  RATE_LIMITED: "Terlalu banyak percobaan. Tunggu beberapa saat lalu coba lagi.",
};

/** Kode yang berarti token tidak bisa dipakai lagi (harus minta token baru). */
const TOKEN_HABIS = new Set([
  "TOKEN_INVALID",
  "TOKEN_EXPIRED",
  "TOKEN_USED",
  "BRANCH_INACTIVE",
  "BRANCH_HAS_ACCOUNT",
]);

interface GalatRegistrasi {
  pesan: string;
  fields: Record<string, string>;
  tokenHabis: boolean;
}

/**
 * galatRegistrasi menerjemahkan error apa pun dari kedua endpoint pendaftaran
 * menjadi pesan yang bisa ditampilkan. Kegagalan jaringan (fetch menolak)
 * bukan ApiClientError sehingga ditangani terpisah.
 */
function galatRegistrasi(err: unknown): GalatRegistrasi {
  if (err instanceof ApiClientError) {
    return {
      pesan: PESAN_TOKEN[err.code] ?? err.message,
      fields: { ...err.fields },
      tokenHabis: TOKEN_HABIS.has(err.code),
    };
  }
  return {
    pesan: "Tidak dapat menghubungi server. Periksa koneksi internet lalu coba lagi.",
    fields: {},
    tokenHabis: false,
  };
}

/**
 * pDaftar — /daftar: pendaftaran akun cabang dua langkah (PRD §3.1
 * CP-REG-02..07, SystemDesign.md §4.1 pDaftar).
 *
 *  - Langkah 1 memvalidasi token ke POST /auth/registration/validate; data
 *    cabang yang ditampilkan di langkah 2 HANYA berasal dari respons backend
 *    (`nama_cabang`, `nama_kepala`, `email`) dan tidak pernah dari input.
 *  - Langkah 2 mengirim POST /auth/register dengan token, telepon, kata sandi,
 *    dan konfirmasinya saja — field read-only sengaja TIDAK diberi `name`
 *    sehingga FormData pun tidak bisa memuatnya, dan backend menolak field tak
 *    dikenal. Identitas cabang/email dengan demikian tidak bisa dipalsukan
 *    lewat payload klien.
 *  - Token hanya hidup di state React (tidak ada localStorage/sessionStorage)
 *    dan tidak pernah ditampilkan kembali setelah langkah 1.
 *  - Setelah 201, pengguna diarahkan ke /masuk dengan pesan sukses (tidak ada
 *    login otomatis; backend tetap otoritas validasi).
 */
export function HalamanDaftar() {
  const navigate = useNavigate();
  const [langkah, setLangkah] = useState<1 | 2>(1);
  const [token, setToken] = useState("");
  const [cabang, setCabang] = useState<DataCabangRegistrasi | null>(null);
  const [password, setPassword] = useState("");
  const [galatToken, setGalatToken] = useState<string | null>(null);
  const [galat, setGalat] = useState<string | null>(null);
  const [galatField, setGalatField] = useState<Record<string, string>>({});
  const [sibuk, setSibuk] = useState(false);
  const [tokenHabis, setTokenHabis] = useState(false);

  function kembaliKeToken() {
    setLangkah(1);
    setCabang(null);
    setToken("");
    setPassword("");
    setGalat(null);
    setGalatToken(null);
    setGalatField({});
    setTokenHabis(false);
  }

  async function lanjut(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sibuk) return;
    const bersih = token.trim().toUpperCase();
    if (!bersih) {
      setGalatToken("Token registrasi wajib diisi.");
      return;
    }
    setSibuk(true);
    setGalatToken(null);
    try {
      const data = await api.post<DataCabangRegistrasi>(
        "/auth/registration/validate",
        { token: bersih },
        { skipCsrf: true },
      );
      setToken(bersih);
      setCabang({
        nama_cabang: data.nama_cabang,
        nama_kepala: data.nama_kepala,
        email: data.email,
      });
      setGalat(null);
      setGalatField({});
      setTokenHabis(false);
      setLangkah(2);
    } catch (err) {
      setGalatToken(galatRegistrasi(err).pesan);
    } finally {
      setSibuk(false);
    }
  }

  async function daftar(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sibuk || !cabang) return;
    const data = new FormData(event.currentTarget);
    const telepon = String(data.get("telepon") ?? "").trim();
    const konfirmasi = String(data.get("password_konfirmasi") ?? "");

    // Umpan balik cepat di klien; backend tetap sumber kebenaran validasi.
    const lokal = periksaKataSandiBaru({
      baru: password,
      konfirmasi,
      fieldBaru: "password",
      fieldKonfirmasi: "password_konfirmasi",
      pesanKonfirmasi: "Konfirmasi kata sandi belum sama.",
    });
    if (!telepon) lokal.telepon = "Nomor telepon wajib diisi.";
    else if (telepon.length > 40) lokal.telepon = "Nomor telepon maksimal 40 karakter.";
    if (Object.keys(lokal).length > 0) {
      setGalatField(lokal);
      setGalat(null);
      return;
    }

    setSibuk(true);
    setGalat(null);
    setGalatField({});
    try {
      await api.post(
        "/auth/register",
        { token, telepon, password, password_konfirmasi: konfirmasi },
        { skipCsrf: true },
      );
      await navigate({ to: "/masuk", search: { daftar: "sukses" } });
    } catch (err) {
      const hasil = galatRegistrasi(err);
      setGalatField(hasil.fields);
      setGalat(hasil.tokenHabis || Object.keys(hasil.fields).length === 0 ? hasil.pesan : null);
      if (hasil.tokenHabis) setTokenHabis(true);
    } finally {
      setSibuk(false);
    }
  }

  let isi: ReactNode;
  if (langkah === 1) {
    isi = (
      <Form onSubmit={(e) => void lanjut(e)}>
        <div>
          <h1>Daftar akun cabang</h1>
          <p className="muted">Masukkan token registrasi dari admin Pusat.</p>
        </div>
        {galatToken ? <Notice tone="danger">{galatToken}</Notice> : null}
        <Field label="Token registrasi" htmlFor="tk">
          <Input
            id="tk"
            name="token"
            value={token}
            onChange={(e) => setToken(e.target.value.toUpperCase())}
            placeholder="GRH-XXXX-XXXX-XXXX-XXXX"
            autoComplete="off"
            autoCapitalize="characters"
            spellCheck={false}
            invalid={galatToken !== null}
            inputClassName="tabular"
          />
        </Field>
        <Button type="submit" variant="primary" block disabled={sibuk}>
          {sibuk ? "Memeriksa token…" : "Lanjutkan"}
        </Button>
        <AuthLinks>
          <Link to="/masuk">Sudah punya akun? Masuk</Link>
        </AuthLinks>
      </Form>
    );
  } else if (cabang) {
    isi = (
      <Form onSubmit={(e) => void daftar(e)}>
        <div>
          <h1>Lengkapi akun</h1>
          <p className="muted">Data cabang diisi dari master data Pusat dan tidak dapat diubah.</p>
        </div>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        <FieldBaca label="Nama cabang" nilai={cabang.nama_cabang} htmlFor="dc-nama" />
        <FieldBaca label="Nama kepala cabang" nilai={cabang.nama_kepala} htmlFor="dc-kepala" />
        <FieldBaca label="Email cabang" nilai={cabang.email} htmlFor="dc-email" />
        <Field
          label="Nomor telepon"
          htmlFor="dc-tel"
          hint="Mis. 08xxxxxxxxxx atau +628xxxxxxxxx."
          error={galatField.telepon}
        >
          <Input
            id="dc-tel"
            name="telepon"
            inputMode="tel"
            autoComplete="tel"
            maxLength={40}
            placeholder="08xxxxxxxxxx"
          />
        </Field>
        <Field
          label="Kata sandi"
          htmlFor="dc-pw"
          hint="Minimal 8 karakter, memuat huruf dan angka."
          error={galatField.password}
        >
          <Input
            id="dc-pw"
            name="password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <PasswordStrength password={password} />
        <Field label="Konfirmasi kata sandi" htmlFor="dc-pwk" error={galatField.password_konfirmasi}>
          <Input
            id="dc-pwk"
            name="password_konfirmasi"
            type="password"
            autoComplete="new-password"
          />
        </Field>
        <Button type="submit" variant="primary" block disabled={sibuk}>
          {sibuk ? "Membuat akun…" : "Buat akun"}
        </Button>
        {tokenHabis ? (
          <Button type="button" variant="secondary" block onClick={kembaliKeToken}>
            Gunakan token lain
          </Button>
        ) : null}
      </Form>
    );
  } else {
    // Tidak seharusnya terjadi (langkah 2 selalu punya data cabang); jaga-jaga.
    isi = (
      <Form onSubmit={(e) => void lanjut(e)}>
        <Notice tone="warning">Data cabang tidak tersedia. Ulangi validasi token.</Notice>
        <Button type="button" variant="secondary" block onClick={kembaliKeToken}>
          Kembali
        </Button>
      </Form>
    );
  }

  return (
    <AuthLayout
      steps={{ total: 2, aktif: langkah }}
      hint={langkah === 1 ? "Token registrasi berlaku 14 hari sejak dibuat admin." : undefined}
    >
      {isi}
    </AuthLayout>
  );
}

/**
 * pLupa — /lupa: satu rute untuk DUA keadaan (PRD §3.2 CP-AUT-03/04).
 *
 *  - tanpa `?token=`: minta tautan atur ulang; respons backend selalu sama
 *    (tidak membocorkan keberadaan email);
 *  - dengan `?token=…` (tautan dari email): buat kata sandi baru; token sekali
 *    pakai dan seluruh sesi lama dicabut setelah berhasil (CP-AUT-07).
 */
export function HalamanLupa({ token }: { token?: string }) {
  return (
    <PasswordResetPage
      token={token}
      namaPortal="portal cabang"
      onKirimTautan={async (email) => {
        await api.post("/auth/password/forgot", { email });
      }}
      onSimpanKataSandi={async (tokenReset, password) => {
        await api.post("/auth/password/reset", { token: tokenReset, password });
      }}
      tautanMasuk={<Link to="/masuk">Kembali ke halaman masuk</Link>}
    />
  );
}
