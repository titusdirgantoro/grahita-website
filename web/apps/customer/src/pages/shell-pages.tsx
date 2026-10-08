import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";

import { api } from "@grahita/api-client";
import {
  Accordion,
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
  Modal,
  Notice,
  PageHead,
  Skeleton,
  SkeletonList,
  Table,
  useToast,
} from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import styles from "./customer-pages.module.css";

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

interface Produk {
  id: string;
  nama: string;
  jenis: string;
  satuan: string;
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
  is_special?: boolean;
  pajak_in?: boolean;
  is_active: boolean;
}

/** pBeranda — /: ringkasan + pesanan terakhir + contoh modal kirim. */
export function BerandaPage() {
  const [modal, setModal] = useState(false);
  const { tampilkan } = useToast();

  return (
    <>
      <PageHead
        title="Beranda"
        sub="Ringkasan pesanan cabang Anda"
        actions={
          <Button variant="primary" icon="plus">
            Buat pesanan
          </Button>
        }
      />

      <div className={styles.kpis}>
        <Card>
          <div className={styles.kpi}>
            <div>
              <p className={styles.lbl}>Pesanan periode ini</p>
              <p className={styles.val}>0</p>
              <span className="caption">Belum ada pesanan bulan ini</span>
            </div>
          </div>
        </Card>
        <Card>
          <div className={styles.kpi}>
            <div>
              <p className={styles.lbl}>Menunggu verifikasi</p>
              <p className={styles.val}>0</p>
              <span className="caption">Bukti transfer diproses admin</span>
            </div>
          </div>
        </Card>
        <Card>
          <div className={styles.kpi}>
            <div>
              <p className={styles.lbl}>Draft belum dikirim</p>
              <p className={styles.val}>0</p>
              <span className="caption">Lanjutkan atau hapus draft</span>
            </div>
          </div>
        </Card>
      </div>

      <div className={styles.stack}>
        <Card>
          <CardHead title="Pesanan terakhir" />
          <EmptyState
            icon="receipt"
            action={
              <Button variant="secondary" icon="plus">
                Buat pesanan pertama
              </Button>
            }
          >
            Belum ada pesanan. Pesanan yang Anda kirim akan tampil di sini beserta statusnya.
          </EmptyState>
        </Card>

        <Card>
          <CardHead title="Alur pesanan" />
          <div className={styles.tools}>
            <Badge tone="draft">Draft</Badge>
            <Badge tone="warning">Menunggu verifikasi</Badge>
            <Badge tone="success">Selesai</Badge>
            <Badge tone="cnc">C&amp;C</Badge>
            <Badge tone="rev">Revisi 1</Badge>
          </div>
          <p className="muted small" style={{ marginTop: 12 }}>
            Contoh chip status dan tombol modal (dipakai saat submit bukti transfer).
          </p>
          <div className={styles.tools} style={{ marginTop: 12 }}>
            <Button variant="secondary" icon="upload-simple" onClick={() => setModal(true)}>
              Contoh modal kirim
            </Button>
            <Button
              variant="ghost"
              icon="check-circle"
              onClick={() => tampilkan("Contoh notifikasi berhasil.")}
            >
              Contoh notifikasi
            </Button>
          </div>
        </Card>

        <Card>
          <CardHead title="Sedang memuat (skeleton)" />
          <SkeletonList rows={2} />
        </Card>
      </div>

      <Modal
        open={modal}
        onClose={() => setModal(false)}
        title="Kirim pesanan"
        footer={
          <>
            <Button variant="secondary" onClick={() => setModal(false)}>
              Batal
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                setModal(false);
                tampilkan("Pesanan dikirim (contoh).");
              }}
            >
              Kirim
            </Button>
          </>
        }
      >
        <Notice tone="info">
          Unggah minimal satu bukti transfer (JPG/PNG/PDF, maks 5 berkas × 5 MB).
        </Notice>
        <p className="muted small">
          Modal ini adalah komponen bersama: fokus terperangkap, Escape menutup, dan fokus kembali
          ke tombol pemicu.
        </p>
      </Modal>
    </>
  );
}

/** pKatalog — /katalog: grid produk aktif dengan harga khusus cabang. */
export function KatalogPage() {
  const { data, memuat, galat } = useApi<Produk[]>("/catalog");

  return (
    <>
      <PageHead title="Katalog" sub="Produk aktif dengan harga khusus cabang Anda" />
      <Card>
        {memuat ? (
          <SkeletonList rows={3} />
        ) : galat ? (
          <Notice tone="danger">{galat}</Notice>
        ) : !data || data.length === 0 ? (
          <EmptyState icon="tag">
            Belum ada produk yang tersedia untuk cabang ini. Hubungi admin Pusat.
          </EmptyState>
        ) : (
          <div className={styles.catGrid}>
            {data.map((p) => (
              <div className={styles.prod} key={p.id}>
                <div className={styles.row}>
                  <span className={styles.nm}>{p.nama}</span>
                  {p.jenis === "sertifikat" ? <Badge tone="info">Sertifikat</Badge> : null}
                </div>
                <span className={styles.price}>{rupiah.format(p.harga)}</span>
                <span className={styles.desc}>
                  {p.deskripsi ?? `Satuan ${p.satuan}`}
                  {p.jenis === "sertifikat" && p.biaya_packaging !== undefined
                    ? ` Biaya packaging ${rupiah.format(p.biaya_packaging)} ditambahkan otomatis per pesanan.`
                    : ""}
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}

/** pInvoice — /invoice: riwayat invoice + unduh PDF (kerangka kartu F1-03). */
export function InvoicePage() {
  return (
    <>
      <PageHead title="Riwayat invoice" sub="Invoice yang sudah selesai beserta versi revisinya" />
      <Card>
        <Table mobileCards caption="Riwayat invoice">
          <thead>
            <tr>
              <th scope="col">Nomor</th>
              <th scope="col">Tanggal</th>
              <th scope="col" className="num">
                Total
              </th>
              <th scope="col">Dokumen</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td colSpan={4}>
                <EmptyState icon="file-text">
                  Belum ada invoice. Invoice tersedia setelah pembayaran diverifikasi admin.
                </EmptyState>
              </td>
            </tr>
          </tbody>
        </Table>
      </Card>
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
                {data.is_cnc ? <Badge tone="cnc">Cash &amp; Carry</Badge> : null}
                {data.is_special ? <Badge tone="info">Special</Badge> : null}
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

/** pTutorial — /tutorial: panduan langkah (accordion native). */
export function TutorialPage() {
  return (
    <>
      <PageHead title="Tutorial" sub="Panduan singkat pemakaian portal cabang" />
      <div className={styles.stack}>
        <Accordion summary="Cara membuat dan mengirim pesanan" open>
          <li>Buka menu Pesanan lalu tekan “Buat pesanan”.</li>
          <li>Tambahkan baris produk dan atur jumlah.</li>
          <li>Simpan sebagai Draft atau lanjutkan ke pengiriman.</li>
          <li>Unggah bukti transfer lalu kirim pesanan.</li>
        </Accordion>
        <Accordion summary="Aturan produk Sertifikat">
          <li>Harga satuan Sertifikat selalu Rp0.</li>
          <li>Biaya packaging ditambahkan otomatis per pesanan.</li>
          <li>Sertifikat tidak boleh dicampur produk reguler.</li>
        </Accordion>
        <Accordion summary="Aturan Cash &amp; Carry">
          <li>Hanya tersedia untuk cabang dengan flag C&amp;C.</li>
          <li>Bukti transfer boleh diunggah menyusul.</li>
          <li>Jatuh tempo dihitung dari tanggal pesanan.</li>
        </Accordion>
      </div>
    </>
  );
}
