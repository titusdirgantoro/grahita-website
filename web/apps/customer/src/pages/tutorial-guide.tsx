import { Link } from "@tanstack/react-router";

import styles from "./tutorial-guide.module.css";

type Penanda = { nomor: number; label: string; x: number; y: number; w: number; h: number };
type Layar = { file: string; alt: string; lebar: number; tinggi: number; penanda: Penanda[] };
type Bagian = {
  id: string;
  judul: string;
  ringkas: string;
  langkah: string[];
  layar?: Layar[];
  tujuan?: { teks: string; ke: "/" | "/katalog" | "/pesanan" | "/pesanan/baru" | "/deposit" | "/tagihan" | "/invoice" | "/profil" };
};

const BAGIAN: Bagian[] = [
  {
    id: "akses",
    judul: "Mulai: akun, Beranda, dan harga cabang",
    ringkas: "Siapkan akun cabang dan periksa produk yang memang dijual kepada cabang Anda.",
    langkah: [
      "Jika belum punya akun, minta token registrasi dari admin Pusat. Di halaman masuk pilih Daftar dengan token, masukkan token, lalu lengkapi telepon dan kata sandi. Token hanya dapat dipakai satu kali.",
      "Masuk dengan email cabang dan kata sandi. Jika lupa kata sandi, gunakan Lupa password di halaman masuk.",
      "Di Beranda, periksa ringkasan pesanan, jumlah yang menunggu verifikasi, dan draft yang belum dikirim. Tombol Buat pesanan membuka editor pesanan baru.",
      "Buka Katalog untuk memeriksa produk aktif dan harga khusus cabang. Harga Expres ditampilkan sebagai tambahan per pcs bila tersedia; memilih Expres dilakukan nanti di tiap baris pesanan.",
    ],
    layar: [
      { file: "beranda.jpg", alt: "Beranda cabang dengan tombol Buat pesanan dan ringkasan status", lebar: 1280, tinggi: 1069, penanda: [
        { nomor: 1, label: "Buat pesanan", x: 85, y: 5, w: 12, h: 4 },
        { nomor: 2, label: "Pesanan menunggu verifikasi", x: 73, y: 11, w: 24, h: 12 },
        { nomor: 3, label: "Draft belum dikirim", x: 60, y: 25, w: 37, h: 13 },
      ] },
      { file: "katalog.jpg", alt: "Katalog menampilkan harga cabang dan tambahan Expres bila tersedia", lebar: 1280, tinggi: 900, penanda: [
        { nomor: 1, label: "Harga produk dan tambahan Expres", x: 21, y: 29, w: 25, h: 22 },
      ] },
    ],
    tujuan: { teks: "Buka Katalog", ke: "/katalog" },
  },
  {
    id: "buat",
    judul: "Buat pesanan per instansi dan produk",
    ringkas: "Satu pesanan boleh berisi banyak baris dengan Nama Instansi yang berbeda.",
    langkah: [
      "Dari Beranda tekan Buat pesanan, atau buka Pesanan lalu Buat pesanan.",
      "Untuk setiap baris, isi Nama Instansi, jumlah pcs, dan Produk. Gunakan Tambah baris untuk instansi atau produk berikutnya. Nama Instansi dapat berbeda pada setiap baris.",
      "Geser tabel item ke kanan untuk memeriksa Harga/Pcs, Produksi, PI, Expres, Bea SPV, Total Tagihan, dan Ket. Nomor serial otomatis dibuat saat pesanan dikirim; kolom Cabang mengikuti akun Anda.",
      "Centang Expres hanya di baris yang membutuhkannya. Tanpa centang, biaya Expres tidak masuk total baris. Jika perlu, isi Ket per baris dan Catatan untuk seluruh pesanan.",
      "Periksa total lalu tekan Simpan draft. Draft dapat dibuka, diubah, atau dihapus sebelum dikirim. Setelah dikirim, item dan harga dikunci.",
      "Produk Sertifikat harus berada dalam pesanan terpisah dari produk reguler. Harga Sertifikat Rp0 dan biaya packaging ditambahkan otomatis satu kali per pesanan.",
    ],
    layar: [
      { file: "pemesanan.jpg", alt: "Editor pesanan memperlihatkan kolom Nama Instansi, jumlah, Produk, Tambah baris, dan Simpan draft", lebar: 1280, tinggi: 900, penanda: [
        { nomor: 1, label: "Nama Instansi per baris", x: 38, y: 29, w: 20, h: 9 },
        { nomor: 2, label: "Jumlah pcs dan Produk", x: 71, y: 29, w: 26, h: 9 },
        { nomor: 3, label: "Tambah baris", x: 22, y: 39, w: 13, h: 6 },
        { nomor: 4, label: "Simpan draft", x: 83, y: 70, w: 13, h: 6 },
      ] },
      { file: "pemesanan-rincian.jpg", alt: "Sisi kanan tabel item pesanan menampilkan pilihan Expres, rincian biaya, total, dan keterangan", lebar: 1280, tinggi: 900, penanda: [
        { nomor: 1, label: "Pilihan Expres pada baris ini", x: 45, y: 30, w: 13, h: 9 },
        { nomor: 2, label: "Bea SPV dan Total Tagihan", x: 59, y: 29, w: 19, h: 9 },
        { nomor: 3, label: "Keterangan baris", x: 78, y: 30, w: 14, h: 8 },
      ] },
    ],
    tujuan: { teks: "Buat pesanan", ke: "/pesanan/baru" },
  },
  {
    id: "kirim",
    judul: "Kirim draft dan pilih cara pembayaran",
    ringkas: "Cara pembayaran dipilih untuk setiap pesanan dari pilihan yang diizinkan Pusat bagi cabang.",
    langkah: [
      "Buka Pesanan, pilih draft, periksa lagi item dan total, lalu tekan Kirim pesanan.",
      "Pada dialog, pilih satu cara pembayaran untuk pesanan ini. Cabang dapat memiliki lebih dari satu pilihan: Transfer di muka, Deposit (Cash & Carry), atau Tempo. Contoh gambar hanya menampilkan Deposit karena itu yang diizinkan pada akun uji.",
      "Baca rincian pembayaran yang muncul sesuai pilihan. Jika wajib transfer, pilih rekening tujuan yang ditampilkan dan lampirkan bukti JPG, PNG, atau PDF. Batas ukuran dan jumlah berkas mengikuti validasi yang ditampilkan sistem.",
      "Tekan Kirim pesanan pada dialog. Sistem membuat nomor pesanan dan serial number, lalu status menjadi Menunggu verifikasi. Admin Pusat akan meninjau pesanan atau bukti; status Perlu review berarti masih perlu pemeriksaan.",
      "Pesanan yang telah dikirim tidak lagi bisa diubah atau dihapus dari portal cabang. Jika ada koreksi, hubungi admin Pusat untuk proses revisi invoice.",
    ],
    layar: [
      { file: "kirim-pesanan.jpg", alt: "Dialog Kirim pesanan dengan pilihan pembayaran, rincian saldo deposit, dan tombol konfirmasi", lebar: 1280, tinggi: 900, penanda: [
        { nomor: 1, label: "Cara pembayaran", x: 30, y: 33, w: 41, h: 13 },
        { nomor: 2, label: "Rincian pembayaran", x: 30, y: 49, w: 41, h: 17 },
        { nomor: 3, label: "Konfirmasi Kirim pesanan", x: 58, y: 79, w: 13, h: 6 },
      ] },
    ],
    tujuan: { teks: "Lihat pesanan", ke: "/pesanan" },
  },
  {
    id: "transfer",
    judul: "Jika memilih Transfer di muka",
    ringkas: "Bukti pembayaran diperlukan saat pesanan dikirim; admin Pusat memverifikasinya.",
    langkah: [
      "Pada dialog Kirim pesanan pilih Transfer di muka. Lihat rekening tujuan yang tersedia untuk cabang, lalu lakukan pembayaran sesuai total pesanan.",
      "Unggah minimal satu bukti transfer berupa JPG, PNG, atau PDF. Jika pembayaran dilakukan dengan beberapa transfer, lampirkan beberapa berkas sesuai batas yang ditampilkan sistem.",
      "Kirim pesanan dan pantau status di Pesanan. Jika masih Menunggu verifikasi atau Perlu review, buka detail pesanan untuk menambahkan bukti pembayaran bila diperlukan.",
      "Setelah admin menyetujui pelunasan, status menjadi Selesai; invoice dan surat jalan tersedia setelah dokumen terbit.",
    ],
    tujuan: { teks: "Lihat pesanan", ke: "/pesanan" },
  },
  {
    id: "deposit",
    judul: "Jika memilih Deposit (Cash & Carry)",
    ringkas: "Saldo terverifikasi dipakai lebih dulu, lalu kekurangannya dapat ditransfer.",
    langkah: [
      "Buka Deposit dan periksa Saldo tersedia. Angka Top-up menunggu verifikasi belum bisa dipakai untuk pesanan.",
      "Untuk menambah saldo, pilih Rekening tujuan, isi Nominal ditransfer, lampirkan Bukti transfer, lalu tekan Kirim top-up. Tunggu admin Pusat memverifikasi hingga riwayat menunjukkan Masuk saldo.",
      "Saat mengirim draft pilih Deposit (Cash & Carry). Dialog menampilkan total, saldo yang dipakai, dan Sisa yang ditransfer.",
      "Jika sisa Rp0, bukti tambahan tidak diperlukan. Jika saldo kurang, transfer sisa dan unggah bukti pada dialog, atau top-up lebih dulu sampai saldo cukup.",
      "Saldo dipotong ketika pesanan dikirim. Setelah admin memverifikasi pembayaran, pesanan menjadi Selesai. Periksa Mutasi saldo untuk melihat perubahan deposit.",
    ],
    layar: [
      { file: "deposit.jpg", alt: "Halaman Deposit dengan saldo, formulir top-up, riwayat, dan mutasi", lebar: 1280, tinggi: 1058, penanda: [
        { nomor: 1, label: "Saldo tersedia", x: 21, y: 11, w: 49, h: 11 },
        { nomor: 2, label: "Rekening, nominal, bukti, dan Kirim top-up", x: 21, y: 25, w: 49, h: 31 },
        { nomor: 3, label: "Riwayat top-up dan status", x: 21, y: 59, w: 49, h: 17 },
      ] },
    ],
    tujuan: { teks: "Buka Deposit", ke: "/deposit" },
  },
  {
    id: "tempo",
    judul: "Jika memilih Tempo dan melunasi tagihan",
    ringkas: "Pesanan disetujui tanpa bayar di muka, kemudian dilunasi melalui Tagihan.",
    langkah: [
      "Pada dialog Kirim pesanan pilih Tempo. Tidak perlu bukti transfer di muka. Jika pilihan Tempo ditahan karena tagihan lewat jatuh tempo, lunasi tunggakan di Tagihan; cara bayar lain yang diizinkan cabang tetap dapat dipilih.",
      "Setelah admin menyetujui, pesanan berstatus Disetujui — belum lunas. Invoice dan surat jalan terbit; utang masuk periode bulan kalender berdasarkan tanggal persetujuan.",
      "Buka Tagihan untuk melihat total piutang, umur piutang, tagihan per periode, tanggal jatuh tempo, dan sisa yang belum dibayar. Surat Tagihan bulanan terbit tanggal 1 bulan berikutnya; invoice tiap pesanan ada di Riwayat invoice.",
      "Untuk pelunasan, isi Nominal yang ditransfer, pilih Rekening tujuan, lampirkan Bukti transfer, lalu tekan Kirim bukti pelunasan. Pembayaran sebagian diperbolehkan.",
      "Setelah admin memverifikasi, pembayaran dialokasikan ke invoice terlama lebih dulu. Pesanan berubah menjadi Selesai ketika nilai invoice tersebut lunas penuh. Kelebihan pembayaran menjadi saldo Tempo setelah disetujui Superadmin.",
      "Jika cabang memiliki aturan tahan pesanan dan ada tunggakan, pilihan Tempo dapat ditahan. Bukti pelunasan yang cukup dan sedang menunggu verifikasi menunda penahanan sementara.",
    ],
    layar: [
      { file: "tagihan.jpg", alt: "Halaman Tagihan dengan ringkasan piutang, periode tagihan, Surat Tagihan, dan formulir pelunasan", lebar: 1280, tinggi: 1294, penanda: [
        { nomor: 1, label: "Ringkasan piutang dan tunggakan", x: 21, y: 9, w: 76, h: 8 },
        { nomor: 2, label: "Tagihan per periode", x: 21, y: 27, w: 76, h: 19 },
        { nomor: 3, label: "Surat Tagihan", x: 21, y: 48, w: 76, h: 9 },
        { nomor: 4, label: "Formulir pelunasan", x: 21, y: 58, w: 76, h: 29 },
      ] },
    ],
    tujuan: { teks: "Buka Tagihan", ke: "/tagihan" },
  },
  {
    id: "dokumen",
    judul: "Pantau status dan unduh dokumen",
    ringkas: "Detail pesanan, invoice, dan surat jalan mengikuti data tiap baris yang sudah dikirim.",
    langkah: [
      "Buka Pesanan untuk memantau Draft, Menunggu verifikasi, Disetujui — belum lunas, dan Selesai. Pilih satu pesanan untuk melihat nomor serial, Nama Instansi, rincian biaya, serta bukti pembayaran.",
      "Buka Riwayat invoice. Gunakan filter tanggal bila perlu, lalu tekan Unduh pada invoice yang dokumennya sudah siap. Status Belum tersedia berarti pesanan belum disetujui atau dokumen belum selesai dibuat.",
      "Surat jalan dan invoice terbit setelah pesanan disetujui. Jika admin melakukan revisi invoice, riwayat menyimpan versi yang relevan; periksa versi dan total sebelum menggunakan dokumen.",
      "Pada dokumen, cocokkan setiap baris: Serial Number, Nama Instansi, Cabang, Jml Pcs, Produk, Harga/Pcs, Produksi, PI, Expres, Bea SPV, Total Tagihan, dan Ket.",
    ],
    layar: [
      { file: "invoice.jpg", alt: "Riwayat invoice dengan filter tanggal, status, versi, dan tombol Unduh", lebar: 1280, tinggi: 1015, penanda: [
        { nomor: 1, label: "Filter tanggal", x: 21, y: 12, w: 76, h: 13 },
        { nomor: 2, label: "Status dan versi invoice", x: 61, y: 33, w: 22, h: 13 },
        { nomor: 3, label: "Unduh dokumen yang siap", x: 84, y: 39, w: 12, h: 7 },
      ] },
    ],
    tujuan: { teks: "Buka Riwayat invoice", ke: "/invoice" },
  },
  {
    id: "akun",
    judul: "Kelola akses akun cabang",
    ringkas: "Data cabang dijaga Pusat; pemilik akun dapat mengganti kata sandi sendiri.",
    langkah: [
      "Tekan kartu akun di bagian bawah navigasi untuk membuka Profil. Di ponsel, gunakan menu Profil pada navigasi bawah.",
      "Periksa nama, email, telepon, dan pilihan pembayaran cabang. Jika ada data yang perlu diubah, hubungi admin Pusat karena data cabang di Profil bersifat hanya baca.",
      "Untuk mengganti kata sandi, isi kata sandi lama dan kata sandi baru beserta konfirmasinya di Profil, lalu simpan. Sesi pada perangkat lain akan diakhiri.",
      "Jika mengalami kendala pesanan atau pembayaran, hubungi admin Pusat dan sertakan nomor pesanan atau periode tagihan agar dapat ditelusuri.",
    ],
    tujuan: { teks: "Buka Profil", ke: "/profil" },
  },
];

function GambarBerpenanda({ layar, judul }: { layar: Layar; judul: string }) {
  return (
    <figure className={styles.figure}>
      <div className={styles.frame}>
        <img src={`/tutorial/${layar.file}`} alt={layar.alt} width={layar.lebar} height={layar.tinggi} loading="lazy" />
        {layar.penanda.map((p) => (
          <span
            key={p.nomor}
            className={styles.marker}
            style={{ left: `${p.x}%`, top: `${p.y}%`, width: `${p.w}%`, height: `${p.h}%` }}
            aria-hidden="true"
          >
            <span>{p.nomor}</span>
          </span>
        ))}
      </div>
      <figcaption>
        <b>{judul}</b>
        <span>Tangkapan layar akun uji produksi. Isi dan pilihan menu dapat berbeda menurut izin cabang.</span>
        <a className={styles.fullImage} href={`/tutorial/${layar.file}`} target="_blank" rel="noopener noreferrer">Buka gambar ukuran penuh <span aria-hidden="true">↗</span></a>
        <ol className={styles.legend} aria-label="Arti penanda pada tangkapan layar">
          {layar.penanda.map((p) => <li key={p.nomor}><span>{p.nomor}</span>{p.label}</li>)}
        </ol>
      </figcaption>
    </figure>
  );
}

export function PanduanLengkap() {
  return (
    <div className={styles.guide}>
      <header className={styles.intro}>
        <p className={styles.eyebrow}>PANDUAN LENGKAP · PORTAL CABANG</p>
        <h2>Alur penggunaan dari awal sampai selesai</h2>
        <p>Ikuti langkah berurutan dari pendaftaran akun, pembuatan pesanan, pilihan pembayaran, hingga pengunduhan dokumen. Kotak bernomor pada tangkapan layar menunjukkan bagian yang perlu diperhatikan.</p>
      </header>
      <nav className={styles.contents} aria-label="Daftar isi tutorial">
        {BAGIAN.map((bagian, i) => <a key={bagian.id} href={`#panduan-${bagian.id}`}><span>{String(i + 1).padStart(2, "0")}</span>{bagian.judul}</a>)}
      </nav>
      {BAGIAN.map((bagian, i) => (
        <section className={styles.section} id={`panduan-${bagian.id}`} key={bagian.id} aria-labelledby={`judul-${bagian.id}`}>
          <div className={styles.sectionHead}>
            <span className={styles.index} aria-hidden="true">{String(i + 1).padStart(2, "0")}</span>
            <div><h3 id={`judul-${bagian.id}`}>{bagian.judul}</h3><p>{bagian.ringkas}</p></div>
          </div>
          <ol className={styles.steps}>{bagian.langkah.map((langkah, j) => <li key={j}>{langkah}</li>)}</ol>
          {bagian.layar?.map((layar) => <GambarBerpenanda key={layar.file} layar={layar} judul={bagian.judul} />)}
          {bagian.tujuan ? <Link className={styles.link} to={bagian.tujuan.ke}>{bagian.tujuan.teks} <span aria-hidden="true">→</span></Link> : null}
        </section>
      ))}
    </div>
  );
}
