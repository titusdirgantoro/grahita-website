import { useState, type FormEvent, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  ChangePasswordForm,
  EmptyState,
  Field,
  Form,
  Input,
  Logo,
  Notice,
  PageHead,
  labelPeran,
} from "@grahita/ui";

import { useSesi } from "../session";
import styles from "./admin-pages.module.css";

export interface Cabang {
  id: string;
  nama: string;
  nama_kepala: string;
  email: string;
  telepon?: string;
  is_cnc: boolean;
  is_special?: boolean;
  pajak_in?: boolean;
  is_active: boolean;
  /** 8 karakter pertama token registrasi aktif (bila ada) — PRD §4.4. */
  token_prefix?: string;
  token_expires_at?: string;
}

/** PenjagaSuperadmin menolak halaman Superadmin untuk Manager (guard rute). */
export function PenjagaSuperadmin({ children }: { children: ReactNode }) {
  const { peran } = useSesi();
  if (peran !== "superadmin") {
    return (
      <>
        <PageHead title="Tidak berwenang" sub="Halaman ini khusus Superadmin" />
        <Card>
          <Notice tone="warning">
            Akun Anda berperan Manager. Menu Superadmin disembunyikan dan halamannya menolak akses.
            Hubungi Superadmin bila membutuhkan akses ini.
          </Notice>
          <div className={styles.row} style={{ marginTop: 16 }}>
            <Button variant="secondary" icon="arrow-left">
              Kembali ke dashboard
            </Button>
          </div>
        </Card>
      </>
    );
  }
  return <>{children}</>;
}

/**
 * pReporting — /reporting (Superadmin): laporan per order line + ekspor XLSX.
 *
 * pDashboard (F1-20) hidup di `./dashboard` karena ia satu kartu tersendiri.
 */
export function ReportingPage() {
  return (
    <PenjagaSuperadmin>
      <PageHead
        title="Reporting"
        sub="Satu baris per order line; ekspor Excel diproses asinkron"
        actions={<Button variant="secondary" icon="download-simple">Ekspor Excel</Button>}
      />
      <Card>
        <EmptyState icon="chart-line">
          Belum ada data laporan pada periode ini. Filter tanggal, cabang, dan status akan tersedia
          di sini.
        </EmptyState>
      </Card>
    </PenjagaSuperadmin>
  );
}

/**
 * pProfil (admin) — /profil: identitas + ganti password (PRD §4.10).
 *
 * Sumber data adalah GET /auth/me lewat konteks sesi (nama, email, role).
 * Nama bisa diubah pemiliknya sendiri lewat PUT /auth/me; EMAIL dan ROLE
 * read-only — hanya Superadmin yang mengubahnya lewat user management, dan
 * backend menolak field itu di endpoint profil (400) maupun untuk akun cabang
 * (403).
 */
export function ProfilAdminPage() {
  const { pengguna, peran, perbaruiPengguna, keluar } = useSesi();
  const navigate = useNavigate();
  const [nama, setNama] = useState<string | null>(null);
  const [galatNama, setGalatNama] = useState<string | null>(null);
  const [suksesNama, setSuksesNama] = useState(false);
  const [sibuk, setSibuk] = useState(false);

  const nilaiNama = nama ?? pengguna?.nama ?? "";

  async function simpanNama(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const baruNama = nilaiNama.trim();
    if (baruNama.length < 2) {
      setGalatNama("Nama minimal 2 karakter.");
      setSuksesNama(false);
      return;
    }
    setSibuk(true);
    setGalatNama(null);
    setSuksesNama(false);
    try {
      const hasil = await api.put<{ user: { id: string; nama: string; email: string; role: string } }>(
        "/auth/me",
        { nama: baruNama },
      );
      perbaruiPengguna({ nama: hasil.user.nama });
      setNama(hasil.user.nama);
      setSuksesNama(true);
    } catch (err) {
      setGalatNama(err instanceof ApiClientError ? err.message : "Gagal menyimpan nama.");
    } finally {
      setSibuk(false);
    }
  }

  async function tindakKeluar() {
    await keluar();
    await navigate({ to: "/masuk", replace: true });
  }

  return (
    <>
      <PageHead title="Profil" sub="Identitas akun admin" />
      <div className={styles.stack}>
        <Card>
          <CardHead title="Akun" tools={<Logo variant="lockup" height={56} />} />
          <Form onSubmit={(e) => void simpanNama(e)}>
            {galatNama ? <Notice tone="danger">{galatNama}</Notice> : null}
            {suksesNama ? <Notice tone="success">Nama profil tersimpan.</Notice> : null}
            <Field label="Nama" htmlFor="adm-nama">
              <Input
                id="adm-nama"
                value={nilaiNama}
                onChange={(e) => setNama(e.target.value)}
                autoComplete="name"
              />
            </Field>
            <Field label="Email (read-only)" htmlFor="adm-email" hint="Email login ditentukan Superadmin.">
              <Input id="adm-email" readOnly value={pengguna?.email ?? ""} />
            </Field>
            <Field label="Role (read-only)" htmlFor="adm-peran" hint="Peran hanya diubah Superadmin di User management.">
              <Input id="adm-peran" readOnly value={labelPeran(peran)} />
            </Field>
            <div className={styles.row}>
              <Button type="submit" variant="primary" disabled={sibuk}>
                {sibuk ? "Menyimpan…" : "Simpan"}
              </Button>
              <Badge tone={peran === "superadmin" ? "rev" : "neutral"}>{labelPeran(peran)}</Badge>
              <Button variant="ghost" icon="sign-out" onClick={() => void tindakKeluar()}>
                Keluar
              </Button>
            </div>
          </Form>
        </Card>
        <Card>
          <CardHead title="Ganti password" />
          <ChangePasswordForm
            idPrefix="adm"
            catatan="Setelah kata sandi berubah, seluruh sesi di perangkat lain ikut diakhiri."
            onSubmit={async (passwordLama, passwordBaru) => {
              await api.post("/auth/password/change", {
                password_lama: passwordLama,
                password_baru: passwordBaru,
              });
            }}
          />
        </Card>
      </div>
    </>
  );
}
