import { useNavigate } from "@tanstack/react-router";

import { api } from "@grahita/api-client";
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
  Skeleton,
  SkeletonList,
} from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import styles from "./customer-pages.module.css";

/**
 * Halaman shell yang ringkas: katalog (F1-08) dan profil cabang (F1-10).
 *
 * Beranda, Riwayat invoice, dan Tutorial punya sumber data/isi sendiri dan
 * hidup di berkas terpisah sejak kartu F1-19: `beranda.tsx`, `invoice.tsx`,
 * `tutorial.tsx`.
 */

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

interface Produk {
  id: string;
  nama: string;
  jenis: string;
  deskripsi?: string;
  harga: number;
  /** Hanya pada produk Sertifikat: biaya packaging per pesanan (CP-KAT-04). */
  biaya_packaging?: number;
}

interface Cabang {
  id: string;
  nama: string;
  nama_kepala: string;
  email: string;
  telepon: string;
  is_cnc: boolean;
  is_tempo?: boolean;
  allow_transfer?: boolean;
  tanggal_bayar?: number;
  pajak_in?: boolean;
  is_active: boolean;
}

/** pKatalog — /katalog: grid produk aktif dengan harga khusus cabang. */
export function KatalogPage() {
  const { data, memuat, galat } = useApi<Produk[]>("/catalog");

  return (
    <>
      <PageHead title="Katalog" sub="Produk aktif dengan harga khusus cabang Anda" />
      {data && data.length > 0 && !memuat && !galat ? (
        // Satu kartu per produk seperti `article.card.prod` prototype, agar
        // produk di layar sempit tidak menyatu tanpa pemisah.
        <div className={styles.catGrid}>
          {data.map((p) => (
            <Card as="article" className={styles.prod} key={p.id}>
              <div className={styles.row}>
                <span className={styles.nm}>{p.nama}</span>
                {p.jenis === "sertifikat" ? <Badge tone="info">Sertifikat</Badge> : null}
              </div>
              <span className={styles.price}>{rupiah.format(p.harga)}</span>
              <span className={styles.desc}>
                {p.deskripsi ?? "Produk katalog cabang"}
                {p.jenis === "sertifikat" && p.biaya_packaging !== undefined
                  ? ` Biaya packaging ${rupiah.format(p.biaya_packaging)} ditambahkan otomatis per pesanan.`
                  : ""}
              </span>
            </Card>
          ))}
        </div>
      ) : (
        <Card>
          {memuat ? (
            <SkeletonList rows={3} />
          ) : galat ? (
            <Notice tone="danger">{galat}</Notice>
          ) : (
            <EmptyState icon="tag">
              Belum ada produk yang tersedia untuk cabang ini. Hubungi admin Pusat.
            </EmptyState>
          )}
        </Card>
      )}
    </>
  );
}

/**
 * pProfil — /profil: data cabang READ-ONLY + ganti password (PRD §3.8, §4.10).
 *
 * Sumber data adalah GET /api/v1/me/branch (cabang dari sesi, bukan parameter
 * klien), jadi akun cabang tidak mungkin membaca profil cabang lain. Tidak ada
 * satu pun field di sini yang dikirim balik ke API: nama, kepala cabang, email,
 * dan telepon dikelola admin Pusat.
 */
export function ProfilPage() {
  const { data, memuat, galat } = useApi<Cabang>("/me/branch");
  const { keluar } = useSesi();
  const navigate = useNavigate();

  async function tindakKeluar() {
    await keluar();
    await navigate({ to: "/masuk", replace: true });
  }

  return (
    <>
      <PageHead title="Profil" sub="Data cabang dikelola admin Pusat. Hubungi Pusat untuk perubahan." />
      <div className={styles.stack}>
        <Card>
          <CardHead title="Data cabang" tools={<Logo variant="lockup" height={56} />} />
          {memuat ? (
            <div className={styles.stack}>
              <Skeleton variant="title" />
              <Skeleton />
              <Skeleton />
            </div>
          ) : galat ? (
            <Notice tone="danger">{galat}</Notice>
          ) : data ? (
            <Form>
              <Field label="Nama cabang" htmlFor="profil-nama">
                <Input id="profil-nama" readOnly value={data.nama} />
              </Field>
              <Field label="Nama kepala cabang" htmlFor="profil-kepala">
                <Input id="profil-kepala" readOnly value={data.nama_kepala} />
              </Field>
              <Field label="Email cabang" htmlFor="profil-email">
                <Input id="profil-email" readOnly value={data.email} />
              </Field>
              <Field
                label="Telepon"
                htmlFor="profil-telepon"
                hint={data.telepon ? undefined : "Belum diisi admin Pusat."}
              >
                <Input id="profil-telepon" readOnly value={data.telepon} />
              </Field>
              <div className={styles.tools}>
                <Badge tone={data.is_active ? "success" : "danger"}>
                  {data.is_active ? "Aktif" : "Nonaktif"}
                </Badge>
                {(data.allow_transfer ?? (!data.is_cnc && !data.is_tempo)) ? <Badge tone="neutral">Transfer di muka</Badge> : null}
                {data.is_cnc ? <Badge tone="cnc">Cash &amp; Carry</Badge> : null}
                {data.is_tempo ? <Badge tone="info">Tempo · bayar tanggal {data.tanggal_bayar}</Badge> : null}
                {data.pajak_in ? <Badge tone="info">PI</Badge> : null}
              </div>
              <div className={styles.tools}>
                <Button variant="secondary" icon="sign-out" onClick={() => void tindakKeluar()}>
                  Keluar
                </Button>
              </div>
            </Form>
          ) : null}
        </Card>

        <Card>
          <CardHead title="Ganti password" />
          <ChangePasswordForm
            idPrefix="profil"
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
