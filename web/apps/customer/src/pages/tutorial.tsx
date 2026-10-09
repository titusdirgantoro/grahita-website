import { type ReactElement, type ReactNode } from "react";

import { Accordion, Card, CardHead, PageHead } from "@grahita/ui";

import styles from "./tutorial.module.css";

/**
 * pTutorial — /tutorial (PRD §3.3, §3.8; kartu F1-19).
 *
 * Empat topik panduan bergambar yang diminta kartu: cara memesan, cara unggah
 * bukti transfer, aturan Sertifikat, dan aturan Cash & Carry. Isi langkahnya
 * mengikuti prototype `customer_v2.html` (TUT) dan aturan di PRD §3.5–§3.7.
 *
 * Ilustrasinya SVG INLINE (bukan berkas/CDN) supaya halaman tetap jalan tanpa
 * aset eksternal — sama seperti ketentuan font/ikon self-host di
 * Architecture.md §3.2 — dan ikut menyesuaikan lebar di mobile maupun desktop.
 */

interface Topik {
  id: string;
  judul: string;
  ringkas: string;
  langkah: string[];
  gambar: () => ReactElement;
}

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

/** Cash & Carry: jam jatuh tempo + dompet pembayaran. */
function GambarCNC() {
  return (
    <Kanvas>
      <circle cx="70" cy="42" r="24" fill="#fff" stroke="var(--brand-300, #c3b6d4)" strokeWidth="2" />
      <path d="M70 30v13l9 6" stroke="var(--brand-500, #906088)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
      <rect x="96" y="46" width="40" height="26" rx="6" fill="var(--brand-900, #504068)" />
      <rect x="96" y="54" width="40" height="4" fill="var(--brand-500, #906088)" />
      <circle cx="126" cy="62" r="3" fill="#fff" />
    </Kanvas>
  );
}

/** TOPIK adalah isi keempat panduan; urutan mengikuti prototype (memesan dulu). */
const TOPIK: Topik[] = [
  {
    id: "memesan",
    judul: "Cara memesan",
    ringkas: "Dari memilih produk sampai pesanan terkirim ke Pusat.",
    gambar: GambarPesan,
    langkah: [
      "Buka menu Pesanan lalu pilih “Buat pesanan”.",
      "Pilih produk dan isi qty; harga satuan terisi otomatis dari harga khusus cabang Anda.",
      "Pilih “Simpan draft” bila belum siap dikirim — draft bisa diubah atau dihapus.",
      "Pilih “Kirim pesanan”, lalu transfer sesuai rekening yang tampil.",
      "Unggah bukti transfer (JPG/PNG/PDF) dan tekan “Kirim pesanan”.",
      "Nomor pesanan muncul dan statusnya menjadi “Menunggu verifikasi”.",
    ],
  },
  {
    id: "bukti",
    judul: "Cara unggah bukti transfer",
    ringkas: "Bukti yang jelas mempercepat verifikasi Pusat.",
    gambar: GambarBukti,
    langkah: [
      "Gunakan foto atau tangkapan layar yang jelas: JPG, PNG, atau PDF.",
      "Satu berkas maksimal 5 MB, dan bisa lebih dari satu berkas untuk pembayaran terpisah.",
      "Non-Cash & Carry wajib menyertakan minimal satu bukti saat mengirim pesanan.",
      "Bukti tambahan bisa diunggah dari detail pesanan selama statusnya masih Menunggu.",
      "Nominal seluruh bukti dijumlahkan saat verifikasi Pusat.",
    ],
  },
  {
    id: "sertifikat",
    judul: "Aturan produk Sertifikat",
    ringkas: "Sertifikat selalu dipesan terpisah dari produk reguler.",
    gambar: GambarSertifikat,
    langkah: [
      "Buat pesanan baru khusus untuk produk Sertifikat.",
      "Harga satuan Sertifikat selalu Rp0 berapa pun jumlah lembarnya.",
      "Biaya packaging ditambahkan otomatis satu baris per pesanan dan tidak bisa diubah.",
      "Produk reguler dipesan di pesanan terpisah; satu pesanan Sertifikat = satu invoice.",
    ],
  },
  {
    id: "cnc",
    judul: "Pesanan Cash & Carry",
    ringkas: "Bayar sebelum jatuh tempo yang dihitung dari tanggal pesanan.",
    gambar: GambarCNC,
    langkah: [
      "Opsi Cash & Carry hanya muncul untuk cabang yang diizinkan Pusat.",
      "Saat mengirim pesanan, aktifkan centang Cash & Carry; bukti transfer boleh menyusul.",
      "Jatuh tempo = tanggal pesanan + jumlah hari C&C yang berlaku (bawaan 7 hari).",
      "Bayar sebelum tanggal jatuh tempo; pesanan baru selesai setelah pembayaran diverifikasi Pusat.",
      "Pengingat dikirim ke email cabang mendekati dan pada hari jatuh tempo.",
    ],
  },
];

export function TutorialPage() {
  return (
    <>
      <PageHead title="Tutorial" sub="Panduan singkat pemakaian portal cabang" />
      <div className={styles.topikList}>
        {TOPIK.map((t, i) => (
          <Card key={t.id} className={styles.topik} as="section">
            <div className={styles.kepala}>
              <t.gambar />
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
        ))}
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
