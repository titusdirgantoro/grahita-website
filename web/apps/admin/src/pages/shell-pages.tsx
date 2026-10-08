import { useState, type FormEvent, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  ChangePasswordForm,
  Chip,
  EmptyState,
  Field,
  Form,
  Input,
  Logo,
  Notice,
  PageHead,
  Segmented,
  Select,
  labelPeran,
} from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import styles from "./admin-pages.module.css";

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

const PERIODE = [
  { value: "hari", label: "Hari ini" },
  { value: "minggu", label: "Minggu ini" },
  { value: "bulan", label: "Bulan ini" },
  { value: "tahun", label: "Tahun ini" },
] as const;

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

/** MiniBars adalah grafik SVG buatan sendiri (bukan library), sesuai §7.2. */
function MiniBars({ nilai, label }: { nilai: number[]; label: string }) {
  const lebarBatang = 10;
  const celah = 6;
  const tinggi = 48;
  const maks = Math.max(...nilai, 1);
  return (
    <div className={styles.chartWrap}>
      <svg
        width={nilai.length * (lebarBatang + celah) - celah}
        height={tinggi}
        role="img"
        aria-label={label}
      >
        {nilai.map((v, i) => {
          const h = v > 0 ? Math.max(4, (tinggi * v) / maks) : 4;
          return (
            <rect
              key={i}
              x={i * (lebarBatang + celah)}
              y={tinggi - h}
              width={lebarBatang}
              height={h}
              rx={2}
              style={{ fill: i === nilai.length - 1 ? "var(--brand-500)" : "var(--brand-200)" }}
            />
          );
        })}
      </svg>
    </div>
  );
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

/** pDashboard — /: filter cabang + periode, chip perlu tindakan, grafik SVG. */
export function DashboardPage() {
  const [periode, setPeriode] = useState<(typeof PERIODE)[number]["value"]>("bulan");
  const [filter, setFilter] = useState("all");
  const [chip, setChip] = useState<Record<string, boolean>>({
    menunggu: true,
    review: true,
    lewat: true,
    email: true,
  });
  const { peran } = useSesi();
  const { data: cabang } = useApi<Cabang[]>("/admin/branches");

  return (
    <>
      <PageHead title="Dashboard" sub="Ringkasan seluruh cabang" />

      <div className={styles.stack}>
        <Card>
          <CardHead
            title="Filter"
            tools={
              <Segmented label="Periode" options={PERIODE} value={periode} onChange={setPeriode} />
            }
          />
          <div className={styles.row}>
            <Field label="Cabang" htmlFor="dash-cabang">
              <Select id="dash-cabang" value={filter} onChange={(e) => setFilter(e.target.value)}>
                <option value="all">Semua cabang</option>
                {(cabang ?? []).map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.nama}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
        </Card>

        <Card>
          <CardHead title="Perlu tindakan" />
          <div className={styles.chips}>
            {(
              [
                ["menunggu", "0 menunggu verifikasi"],
                ["review", "0 perlu review"],
                ["lewat", "0 C&C lewat jatuh tempo"],
                ["email", "0 email gagal"],
              ] as const
            ).map(([kunci, teks]) => (
              <Chip
                key={kunci}
                pressed={chip[kunci]}
                onToggle={(v) => setChip((c) => ({ ...c, [kunci]: v }))}
                onRemove={() => setChip((c) => ({ ...c, [kunci]: false }))}
                removeLabel={`Sembunyikan: ${teks}`}
              >
                {teks}
              </Chip>
            ))}
          </div>
        </Card>

        <div className={styles.kpis}>
          <Card>
            <p className={styles.lbl}>Nilai pesanan {periode} ini</p>
            <p className={styles.val}>{peran === "superadmin" ? rupiah.format(0) : "0 pesanan"}</p>
            <span className="caption">
              {peran === "superadmin" ? "Superadmin melihat rupiah" : "Manager melihat jumlah"}
            </span>
          </Card>
          <Card>
            <p className={styles.lbl}>Pesanan selesai</p>
            <p className={styles.val}>0</p>
            <span className="caption">Belum ada pesanan diverifikasi</span>
          </Card>
          <Card>
            <p className={styles.lbl}>Cabang aktif</p>
            <p className={styles.val}>{cabang ? cabang.filter((c) => c.is_active).length : "…"}</p>
            <span className="caption">Dari master cabang</span>
          </Card>
        </div>

        <Card>
          <CardHead title="Tren 12 bulan" />
          <MiniBars nilai={Array.from({ length: 12 }, () => 0)} label="Tren pesanan 12 bulan" />
          <p className="muted small" style={{ marginTop: 8 }}>
            Grafik SVG buatan sendiri; data menyusul pada kartu Reporting/Dashboard.
          </p>
        </Card>

        <Card>
          <CardHead title="Ringkasan status pembayaran" />
          <div className={styles.row}>
            <Badge tone="success">0 terverifikasi</Badge>
            <Badge tone="warning">0 menunggu</Badge>
            <Badge tone="danger">0 perlu review</Badge>
          </div>
        </Card>
      </div>
    </>
  );
}

/** pReporting — /reporting (Superadmin): laporan per order line + ekspor XLSX. */
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
