import { type ReactElement, type ReactNode } from "react";

import { Accordion, Card, CardHead, PageHead } from "@grahita/ui";

import { useApi } from "../useApi";
import styles from "./tutorial.module.css";

/**
 * pTutorial — /tutorial (PRD §3.3, §3.8; kartu F1-19, diperbarui kartu F2-27).
 *
 * Empat topik panduan bergambar: cara memesan, cara unggah bukti transfer,
 * aturan Sertifikat, dan aturan Cash & Carry. Isi langkahnya mengikuti
 * prototype `customer_v2.html` (TUT) dan aturan di PRD §3.5–§3.7.
 *
 * Sejak F2-27 kontennya TIDAK lagi ditanam di bundel: halaman ini membacanya
 * dari `GET /api/v1/tutorial`, sehingga Superadmin dapat memperbarui tutorial
 * dan cabang yang me-refresh halaman langsung melihat versinya — tanpa build
 * atau deploy portal. Selama permintaan belum selesai (atau bila gagal,
 * misalnya koneksi terputus), halaman tetap menampilkan empat topik bawaan di
 * bawah supaya panduan tidak pernah kosong; begitulah "konten awal tetap
 * tersedia" juga berlaku di sisi klien.
 *
 * Server mengirim TEKS BIASA (disanitasi) plus KUNCI ilustrasi (`ikon`).
 * Ilustrasinya SVG INLINE milik aplikasi — bukan berkas/CDN dan bukan markup
 * dari data — supaya tidak ada HTML arbitrer yang bisa masuk lewat konten dan
 * halaman tetap jalan tanpa aset eksternal (Architecture.md §3.2), sekaligus
 * ikut menyesuaikan lebar di mobile maupun desktop.
 */

/** Topik yang dikirim server. */
interface TopikApi {
  id: string;
  ikon: string;
  judul: string;
  ringkas: string;
  langkah: string[];
}

/** Topik bawaan: dipakai sebelum data server tiba dan bila permintaan gagal. */
const TOPIK_MEMESAN: TopikApi = {
  id: "bawaan-memesan",
  ikon: "memesan",
  judul: "Cara memesan",
  ringkas: "Dari memilih produk sampai pesanan terkirim ke Pusat.",
  langkah: [
    "Buka menu Pesanan lalu pilih “Buat pesanan”.",
    "Pilih produk dan isi qty; harga satuan terisi otomatis dari harga khusus cabang Anda.",
    "Pilih “Simpan draft” bila belum siap dikirim — draft bisa diubah atau dihapus.",
    "Pilih “Kirim pesanan”, lalu transfer sesuai rekening yang tampil.",
    "Unggah bukti transfer (JPG/PNG/PDF) dan tekan “Kirim pesanan”.",
    "Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”.",
  ],
};

const TOPIK_BUKTI: TopikApi = {
  id: "bawaan-bukti",
  ikon: "bukti",
  judul: "Cara unggah bukti transfer",
  ringkas: "Bukti yang jelas mempercepat verifikasi Pusat.",
  langkah: [
    "Gunakan foto atau tangkapan layar yang jelas: JPG, PNG, atau PDF.",
    "Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.",
    "Non-Cash & Carry wajib menyertakan minimal satu bukti saat mengirim pesanan.",
    "Bukti tambahan bisa diunggah dari detail pesanan selama statusnya masih Menunggu.",
    "Nominal seluruh bukti dijumlahkan saat verifikasi Pusat.",
  ],
};

const TOPIK_SERTIFIKAT: TopikApi = {
  id: "bawaan-sertifikat",
  ikon: "sertifikat",
  judul: "Aturan produk Sertifikat",
  ringkas: "Sertifikat selalu dipesan terpisah dari produk reguler.",
  langkah: [
    "Buat pesanan baru khusus untuk produk Sertifikat.",
    "Harga satuan Sertifikat selalu Rp0 berapa pun jumlah lembarnya.",
    "Biaya packaging ditambahkan otomatis satu baris per pesanan dan tidak bisa diubah.",
    "Produk reguler dipesan di pesanan terpisah; satu pesanan Sertifikat = satu invoice.",
  ],
};

const TOPIK_CNC: TopikApi = {
  id: "bawaan-cnc",
  ikon: "cnc",
  judul: "Pesanan Cash & Carry",
  ringkas: "Pesanan dibayar dari saldo deposit cabang yang sudah diverifikasi Pusat.",
  langkah: [
    "Cash & Carry berlaku untuk cabang yang ditetapkan Pusat; pesanannya dibayar dari saldo deposit.",
    "Top-up deposit lewat menu Deposit: transfer ke rekening cabang lalu unggah buktinya.",
    "Saldo bertambah setelah top-up diverifikasi Pusat; top-up yang masih menunggu belum bisa dipakai.",
    "Saat pesanan dikirim, saldo deposit langsung dipotong sebesar total pesanan.",
    "Bila saldo kurang, transfer sisanya dan unggah buktinya saat mengirim pesanan.",
    "Pesanan selesai setelah pembayarannya diverifikasi Pusat.",
  ],
};

/** TOPIK_BAWAAN adalah isi empat panduan sebelum/ketika API tidak tersedia. */
const TOPIK_BAWAAN: TopikApi[] = [TOPIK_MEMESAN, TOPIK_BUKTI, TOPIK_SERTIFIKAT, TOPIK_CNC];

/** Bidang hias: bingkai ilustrasi dengan latar token brand. */
function Kanvas({ children }: { children: ReactNode }) {
  return (
    <svg
      className={styles.ilustrasi}
      viewBox="0 0 160 90"
      role="img"
      aria-hidden="true"
      focusable="false"
      data-testid="ilustrasi-topik"
    >
      <rect x="0" y="0" width="160" height="90" rx="16" fill="var(--brand-50, #f4f1f7)" />
      {children}
    </svg>
  );
}

/** Pesan: dokumen pesanan + tombol tambah + garis baris produk. */
function GambarPesan() {
  return (
    <Kanvas>
      <rect x="52" y="18" width="56" height="56" rx="8" fill="#fff" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <rect x="62" y="30" width="36" height="5" rx="2.5" fill="var(--brand-500, #906088)" />
      <rect x="62" y="42" width="36" height="5" rx="2.5" fill="var(--line, #e3e0e8)" />
      <rect x="62" y="54" width="24" height="5" rx="2.5" fill="var(--line, #e3e0e8)" />
      <circle cx="118" cy="62" r="16" fill="var(--brand-900, #504068)" />
      <path d="M118 55v14M111 62h14" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
    </Kanvas>
  );
}

/** Bukti transfer: kartu unggah + awan/berkas naik. */
function GambarBukti() {
  return (
    <Kanvas>
      <rect x="40" y="34" width="80" height="42" rx="8" fill="#fff" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <path d="M80 62V42M72 50l8-8 8 8" stroke="var(--brand-500, #906088)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
      <rect x="54" y="66" width="52" height="5" rx="2.5" fill="var(--line, #e3e0e8)" />
      <circle cx="42" cy="26" r="9" fill="var(--brand-300, #c3b6d4)" />
      <circle cx="120" cy="24" r="6" fill="var(--brand-500, #906088)" />
    </Kanvas>
  );
}

/** Sertifikat: lembar bersegel dengan pita. */
function GambarSertifikat() {
  return (
    <Kanvas>
      <rect x="48" y="16" width="56" height="60" rx="6" fill="#fff" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <rect x="58" y="28" width="36" height="4" rx="2" fill="var(--line, #e3e0e8)" />
      <rect x="58" y="38" width="36" height="4" rx="2" fill="var(--line, #e3e0e8)" />
      <rect x="58" y="48" width="22" height="4" rx="2" fill="var(--line, #e3e0e8)" />
      <circle cx="104" cy="62" r="12" fill="var(--brand-500, #906088)" />
      <path d="M99 62l4 4 7-8" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M74 76v10l6-4 6 4V76" fill="var(--brand-900, #504068)" />
    </Kanvas>
  );
}

/** Cash & Carry: dompet deposit cabang yang membayar pesanan. */
function GambarCNC() {
  return (
    <Kanvas>
      <rect x="52" y="28" width="56" height="38" rx="8" fill="var(--brand-900, #504068)" />
      <rect x="52" y="40" width="56" height="5" fill="var(--brand-500, #906088)" />
      <circle cx="96" cy="56" r="4" fill="#fff" />
      <rect x="112" y="36" width="22" height="30" rx="4" fill="#fff" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <rect x="117" y="44" width="12" height="3" rx="1.5" fill="var(--brand-500, #906088)" />
      <rect x="117" y="52" width="12" height="3" rx="1.5" fill="var(--line, #e3e0e8)" />
    </Kanvas>
  );
}

/** Umum: buku panduan untuk topik tanpa ilustrasi khusus. */
function GambarUmum() {
  return (
    <Kanvas>
      <rect x="46" y="20" width="68" height="52" rx="6" fill="#fff" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <path d="M80 20v52" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <rect x="54" y="32" width="20" height="4" rx="2" fill="var(--brand-500, #906088)" />
      <rect x="54" y="42" width="20" height="4" rx="2" fill="var(--line, #e3e0e8)" />
      <rect x="86" y="32" width="20" height="4" rx="2" fill="var(--line, #e3e0e8)" />
      <rect x="86" y="42" width="20" height="4" rx="2" fill="var(--line, #e3e0e8)" />
    </Kanvas>
  );
}

/**
 * IKON memetakan kunci ilustrasi dari server ke gambar bawaan aplikasi.
 * Kunci yang tidak dikenal memakai ilustrasi umum — server sudah membatasi
 * nilainya, pemetaan ini hanya jaring pengaman di sisi tampilan.
 */
const IKON: Record<string, () => ReactElement> = {
  memesan: GambarPesan,
  bukti: GambarBukti,
  sertifikat: GambarSertifikat,
  cnc: GambarCNC,
  umum: GambarUmum,
};

/** ilustrasiTopik memilih gambar bawaan sesuai kunci yang dikirim server. */
export function ilustrasiTopik(ikon: string): () => ReactElement {
  return IKON[ikon] ?? GambarUmum;
}

export function TutorialPage() {
  // Permintaan gagal (mis. koneksi terputus) TIDAK mengosongkan halaman:
  // panduan bawaan tetap tampil supaya cabang tidak kehilangan panduannya.
  const { data } = useApi<{ topik: TopikApi[] }>("/tutorial");
  const topik = data?.topik?.length ? data.topik : TOPIK_BAWAAN;

  return (
    <>
      <PageHead title="Tutorial" sub="Panduan singkat pemakaian portal cabang" />
      <div className={styles.topikList}>
        {topik.map((t, i) => {
          const Gambar = ilustrasiTopik(t.ikon);
          return (
            <Card key={t.id} className={styles.topik} as="section">
              <div className={styles.kepala}>
                <Gambar />
                <div className={styles.teks}>
                  <h2 className={styles.judul}>{t.judul}</h2>
                  <p className={styles.ringkas}>{t.ringkas}</p>
                </div>
              </div>
              <Accordion summary="Langkah-langkah" open={i === 0}>
                {t.langkah.map((l) => (
                  <li key={l}>{l}</li>
                ))}
              </Accordion>
            </Card>
          );
        })}
        <Card>
          <CardHead title="Butuh bantuan lain?" />
          <p className="muted small">
            Panduan ini bisa dibaca di ponsel maupun desktop. Bila ada yang belum jelas, hubungi admin
            Pusat — data kontak ada di halaman Profil cabang Anda.
          </p>
        </Card>
      </div>
    </>
  );
}
