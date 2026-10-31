import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";

import { Card, Icon, PageHead } from "@grahita/ui";

import catalog from "./documentation-scenarios.json";
import styles from "./documentation.module.css";

type Section = "panduan" | "teknis";

interface Scenario {
  id: string;
  priority: string;
  layer: string;
  action: string;
  expected: string;
}

interface ScenarioGroup {
  title: string;
  items: Scenario[];
}

const scenarioGroups = catalog as ScenarioGroup[];

const guide = [
  {
    id: "akses",
    title: "1. Akses dan akun",
    audience: "Superadmin · Manager · Cabang",
    steps: [
      "Superadmin membuat master cabang, memilih cara pembayaran yang diizinkan, menetapkan rekening tujuan, lalu membuat token registrasi. Token dikirim lewat jalur komunikasi resmi dan hanya berlaku satu kali.",
      "Cabang membuka halaman daftar, memvalidasi token, memeriksa identitas cabang yang terkunci, lalu mengisi telepon dan kata sandi. Setelah aktivasi, cabang masuk melalui portal cabang.",
      "Admin masuk melalui portal admin. Superadmin dapat mengundang admin, mengubah peran dan flag penerima email; Manager hanya melihat fungsi sesuai haknya.",
      "Jika lupa kata sandi, gunakan tautan Lupa kata sandi. Di Profil, pemilik akun dapat mengganti kata sandi; admin juga dapat mengganti nama tampilannya. Mengganti kata sandi mencabut sesi lain.",
    ],
    note: "Token salah, kedaluwarsa, atau sudah dipakai tidak dapat digunakan ulang. Cabang dan akun yang nonaktif tidak dapat masuk.",
  },
  {
    id: "master",
    title: "2. Siapkan cabang, produk, harga, dan rekening",
    audience: "Superadmin · Manager",
    steps: [
      "Di Cabang, isi identitas dan pilih satu atau beberapa izin pembayaran: Transfer di muka, Deposit (Cash & Carry), dan Tempo. Tiap pesanan nantinya memilih tepat satu cara bayar.",
      "Untuk Tempo, isi tanggal bayar 1–28 dan tentukan apakah pesanan Tempo ditahan saat ada tagihan lewat jatuh tempo. Izin dan tanggal bayar disalin ketika pesanan dikirim.",
      "Di Rekening bank, Superadmin mengelola rekening aktif dan penugasan per cabang. Cabang hanya melihat rekening efektif miliknya.",
      "Di Katalog product, kelola produk aktif, tipe Sertifikat, urutan, dan harga tiap cabang. Tarif dapat memuat Produksi, PI, Expres, dan Bea SPV; harga yang disetujui pesanan menjadi snapshot.",
      "Di Pengaturan, Superadmin menetapkan biaya packaging, batas unggah, masa token/reset, izin revisi Manager, serta opsi OCR. Kunci Gemini 2.5 Flash-Lite hanya diisi dan dikelola Superadmin.",
    ],
    note: "Produk tanpa harga cabang tidak tersedia di katalog cabang tersebut. Perubahan master tidak menghitung ulang pesanan yang telah dikirim.",
  },
  {
    id: "pesanan",
    title: "3. Buat dan kirim pesanan cabang",
    audience: "Cabang",
    steps: [
      "Buka Katalog untuk melihat produk dan harga cabang sendiri, lalu buat draft dari Pesanan. Draft dapat disimpan, diubah, dihapus, dan dibuka kembali.",
      "Isi tiap baris dengan Nama Instansi, produk, jumlah pcs, dan Ket bila perlu. Satu pesanan boleh memiliki banyak baris dengan nama instansi berbeda. Serial Number YYYYMMDDXXX dibuat sistem saat submit.",
      "Periksa urutan rincian: No → Serial Number → Nama Instansi → Cabang → Jml Pcs → Produk → Harga/Pcs → Produksi → PI → Expres → Bea SPV → Total Tagihan → Ket.",
      "Pilih satu cara pembayaran yang diizinkan dan periksa total serta rekening. Unggah bukti bila mode dan sisa pembayaran mewajibkannya, kemudian kirim. Nomor invoice dan snapshot harga dibuat saat submit dalam waktu WIB.",
      "Setelah terkirim, baris tidak bisa diedit oleh cabang. Pantau status, timeline, bukti, invoice, dan dokumen dari detail pesanan; perubahan sesudah submit dilakukan melalui revisi admin.",
    ],
    note: "Produk Sertifikat memakai harga Rp0 dan satu baris biaya packaging untuk pesanan Sertifikat. Jangan mencampur Sertifikat dengan produk reguler dalam satu pesanan.",
  },
  {
    id: "transfer",
    title: "4. Jalur Transfer di muka",
    audience: "Cabang · Manager · Superadmin",
    steps: [
      "Cabang memilih Transfer di muka dan mengunggah bukti ke rekening efektif sebelum submit.",
      "Manager atau Superadmin membuka Riwayat pemesanan, meninjau bukti dan rekening, lalu menetapkan nominal yang benar. Bukti yang kurang, kabur, atau tidak cocok masuk Perlu review beserta alasan.",
      "OCR Gemini dapat membantu membaca bukti bila Superadmin mengaktifkannya dan kunci telah diverifikasi. Keputusan dapat tetap ditinjau manual; kegagalan OCR tidak menyelesaikan pembayaran.",
      "Setelah nilai pembayaran memenuhi total, pesanan menjadi Selesai. Worker menerbitkan invoice dan surat jalan serta mengirim email sesuai flag penerima.",
    ],
    note: "Bukti berlebih yang dialihkan ke Deposit memerlukan keputusan Superadmin. Verifikasi berulang tidak boleh membuat pembayaran ganda.",
  },
  {
    id: "deposit",
    title: "5. Jalur Deposit (Cash & Carry)",
    audience: "Cabang · Manager · Superadmin",
    steps: [
      "Cabang membuka Deposit, melihat saldo dan mutasi, lalu mengunggah bukti top-up bila ingin menambah saldo.",
      "Admin meninjau top-up. Hanya Superadmin dapat memverifikasi kredit saldo; Manager dapat meninjau atau menolak sesuai haknya.",
      "Saat submit, sistem memakai saldo yang tersedia. Jika saldo kurang dari total, cabang mengunggah bukti untuk selisihnya; pesanan menjadi Selesai setelah seluruh sumber pembayaran disetujui.",
      "Cocokkan saldo, jurnal, alokasi, dan sisa pesanan. Reversal dan revisi yang menyentuh Deposit memerlukan alasan dan otorisasi yang sesuai.",
    ],
    note: "Saldo dan harga bisa berubah sejak form dibuka. Jika muncul konflik versi, muat ulang sebelum melanjutkan.",
  },
  {
    id: "tempo",
    title: "6. Jalur Tempo dan pelunasan",
    audience: "Cabang · Manager · Superadmin",
    steps: [
      "Cabang memilih Tempo dan submit tanpa bukti transfer. Jika ada tagihan lewat jatuh tempo dan toggle tahan aktif, hanya submit Tempo yang tertahan; draft, Transfer, dan Deposit tetap mengikuti izin masing-masing.",
      "Manager atau Superadmin menyetujui pesanan Tempo. Status menjadi Disetujui — belum lunas; invoice dan surat jalan langsung terbit. Periode tagihan mengikuti tanggal submit WIB, dan tanggal bayar berasal dari snapshot cabang.",
      "Pada tanggal 1 bulan berikutnya, worker menerbitkan Surat Tagihan per cabang/periode. Cabang membuka Tagihan untuk melihat total, sisa, status, PDF, dan mengunggah bukti pelunasan sebagian atau penuh.",
      "Admin memverifikasi pelunasan. Nominal dialokasikan ke invoice tertua lebih dahulu lintas periode. Pesanan berubah menjadi Selesai setelah lunas; lebih bayar hanya dapat menjadi kredit Tempo melalui Superadmin.",
      "Pengingat dikirim pada H, H+1, H+7. H-3 dikirim untuk tanggal bayar 4–28 dan dilewati untuk tanggal bayar 1–3. Setelah tanggal jatuh tempo, status lewat jatuh tempo dimulai H+1 WIB.",
    ],
    note: "Bukti pending yang cukup menutup seluruh tunggakan memberi masa tenggang dua hari kerja dan tidak dapat diperpanjang dengan unggahan ulang. Kredit Tempo terpisah dari saldo Deposit.",
  },
  {
    id: "dokumen",
    title: "7. Dokumen, revisi, dan riwayat",
    audience: "Cabang · Manager · Superadmin",
    steps: [
      "Cabang melihat Riwayat invoice, invoice PDF, dan detail pesanan. Admin melihat invoice, surat jalan PDF/XLSX, bukti, serta timeline yang berhubungan dengan pesanan.",
      "Jika ada koreksi setelah submit, admin membuat revisi beralasan. Nomor invoice tetap, versi dokumen bertambah, dan versi lama tetap dapat diunduh.",
      "Nama Instansi, serial, rincian Produksi/PI/Expres/Bea SPV, Ket, subtotal, dan total harus konsisten antara editor, detail admin/cabang, PDF, dan laporan.",
      "Pada Tempo, revisi mengubah charge dan sisa tagihan. Pada Deposit, koreksi yang menyentuh alokasi wajib mengikuti rencana reversal yang sah.",
    ],
    note: "Dokumen mungkin memerlukan waktu render. Jika status belum siap atau email gagal, periksa antrean dan ulangi proses melalui jalur admin yang tersedia.",
  },
  {
    id: "pantau",
    title: "8. Pantau, laporkan, dan kelola sistem",
    audience: "Manager · Superadmin",
    steps: [
      "Dashboard dan Riwayat pemesanan menampilkan order, status verifikasi, filter, dan item yang perlu tindakan. Deposit cabang menampilkan top-up, sedangkan Tagihan cabang menampilkan piutang, umur piutang, pelunasan, dan Surat Tagihan.",
      "Superadmin memakai Reporting untuk melihat satu baris per order line serta membuat ekspor XLSX. Ekspor piutang tersedia di Tagihan cabang.",
      "User management mengelola akun cabang/admin sesuai peran. Pengaturan juga memuat tutorial cabang, status OCR, dan operasi cadangan. Audit log memuat aktor, waktu, perubahan, dan alasan.",
      "Untuk masalah akses, periksa status akun, peran, sesi, dan rekening yang ditugaskan. Untuk masalah transaksi, periksa timeline, verifikasi, job, dokumen, dan audit sebelum mengulang aksi.",
    ],
    note: "Backup lokal terenkripsi dan restore lokal telah diuji. Salinan offsite dan restore dari offsite masih perlu disediakan agar pemulihan dari kegagalan VPS lengkap.",
  },
] as const;

const paymentFlows = [
  { title: "Transfer", icon: "bank", steps: ["Bukti transfer", "Verifikasi admin / OCR opsional", "Lunas → dokumen"] },
  { title: "Deposit", icon: "wallet", steps: ["Saldo + bukti bila kurang", "Top-up oleh Superadmin", "Lunas → dokumen"] },
  { title: "Tempo", icon: "calendar-check", steps: ["Setujui tanpa bukti", "Invoice + Surat Jalan", "Tagihan → FIFO → lunas"] },
] as const;

function ScenarioCatalog() {
  const [query, setQuery] = useState("");
  const filtered = useMemo(() => {
    const term = query.trim().toLocaleLowerCase("id-ID");
    return scenarioGroups.map((group) => ({
      ...group,
      items: term
        ? group.items.filter((item) => `${item.id} ${item.priority} ${item.layer} ${item.action} ${item.expected}`.toLocaleLowerCase("id-ID").includes(term))
        : group.items,
    })).filter((group) => group.items.length > 0);
  }, [query]);
  const count = filtered.reduce((sum, group) => sum + group.items.length, 0);

  return (
    <Card as="section" className={styles.article}>
      <h2 id="skenario">Katalog 162 skenario end to end</h2>
      <p>Katalog ini memuat seluruh perjalanan, variasi, hasil yang diharapkan, dan kasus kegagalan dari rencana uji sistem. Cari berdasarkan ID, fitur, peran, atau kata kunci. Ini adalah daftar skenario, bukan status hasil pengujian.</p>
      <label className={styles.searchLabel} htmlFor="cari-skenario">Cari skenario</label>
      <input id="cari-skenario" className={styles.search} type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Contoh: Tempo, bukti, ORD-11" />
      <p className={styles.count} role="status">{count} dari 162 skenario ditampilkan</p>
      {filtered.length === 0 ? <p>Tidak ada skenario yang cocok. Coba kata kunci lain.</p> : (
        <div className={styles.scenarioGroups}>
          {filtered.map((group, index) => (
            <details key={group.title} open={query.length > 0 || index === 0}>
              <summary>{group.title} <span>{group.items.length} skenario</span></summary>
              <ol className={styles.scenarioList}>
                {group.items.map((item) => (
                  <li key={item.id}>
                    <div className={styles.scenarioMeta}><strong>{item.id}</strong><span>{item.priority} · {item.layer}</span></div>
                    <p>{item.action.replaceAll("`", "")}</p>
                    {item.expected ? <p className={styles.expected}><b>Hasil:</b> {item.expected.replaceAll("`", "")}</p> : null}
                  </li>
                ))}
              </ol>
            </details>
          ))}
        </div>
      )}
    </Card>
  );
}

function Guide() {
  return (
    <>
      <Card as="section" className={styles.article}>
        <h2 id="alur">Alur dari awal hingga selesai</h2>
        <p>Mulai dari data master, cabang membuat draft dan memilih satu cara bayar per pesanan. Setiap jalur berakhir pada dokumen dan riwayat; Tempo juga melewati pelunasan piutang.</p>
        <ol className={styles.flowTop} aria-label="Alur utama pesanan">
          {["Siapkan cabang, harga, rekening", "Cabang buat draft per instansi", "Pilih cara bayar dan kirim", "Admin proses pembayaran", "Dokumen, tagihan, laporan"].map((step) => <li key={step}>{step}</li>)}
        </ol>
        <div className={styles.flowBranches} aria-label="Tiga jalur pembayaran">
          {paymentFlows.map((flow) => (
            <article key={flow.title}>
              <h3><Icon name={flow.icon} /> {flow.title}</h3>
              <ol>{flow.steps.map((step) => <li key={step}>{step}</li>)}</ol>
            </article>
          ))}
        </div>
      </Card>
      <nav className={styles.toc} aria-label="Isi panduan">
        {guide.map((section) => <a key={section.id} href={`#${section.id}`}>{section.title}</a>)}
        <a href="#skenario">162 skenario</a>
      </nav>
      {guide.map((section) => (
        <Card as="section" className={styles.article} key={section.id}>
          <h2 id={section.id}>{section.title}</h2>
          <p className={styles.audience}>{section.audience}</p>
          <ol className={styles.steps}>{section.steps.map((step) => <li key={step}>{step}</li>)}</ol>
          <p className={styles.note}><b>Perhatikan:</b> {section.note}</p>
        </Card>
      ))}
      <ScenarioCatalog />
    </>
  );
}

const entities = [
  ["branches → branch_prices / branch_tariff_versions", "Izin bayar, rekening, dan tarif efektif tiap cabang"],
  ["branches → orders → order_lines", "Satu cabang memiliki banyak pesanan; tiap baris menyimpan instansi, serial, qty, dan snapshot komponen harga"],
  ["orders → payment_proofs → verification_logs", "Bukti, keputusan verifikasi, dan jejak audit pembayaran"],
  ["branches → branch_deposit_accounts → branch_deposit_transactions", "Saldo dan mutasi Deposit dengan sumber serta versi jurnal"],
  ["orders → tempo_charges → tempo_allocations", "Piutang dan alokasi pelunasan FIFO lintas invoice"],
  ["tempo_receipts → tempo_allocations / tempo_credit_entries", "Bukti pelunasan dan kredit lebih bayar yang terpisah dari Deposit"],
  ["tempo_statements → tempo_statement_lines", "Surat Tagihan berversi per cabang dan periode"],
  ["orders → documents / invoice_revisions", "PDF/XLSX berversi; nomor invoice dan versi historis dipertahankan"],
] as const;

function Technical() {
  return (
    <>
      <Card as="section" className={styles.article}>
        <h2 id="arsitektur">Arsitektur sistem yang berjalan</h2>
        <p>Dua aplikasi React 19 + TypeScript + Vite memakai paket UI bersama. Keduanya dilayani oleh Caddy dan berbicara ke satu API Go berbasis chi. API dan worker berbagi PostgreSQL; pekerjaan asinkron disimpan oleh River di database yang sama.</p>
        <div className={styles.architecture} role="img" aria-label="Portal cabang dan admin melalui Caddy menuju API Go dan worker River. API memakai PostgreSQL, MinIO, Gotenberg, SMTP, dan Gemini opsional.">
          <div className={styles.archPortals}>
            <div><b>Portal Cabang</b><span>React · Vite</span></div>
            <div><b>Portal Admin</b><span>React · Vite</span></div>
          </div>
          <span className={styles.archArrow} aria-hidden="true">↓</span>
          <div><b>Caddy</b><span>TLS · dua host · proxy API</span></div>
          <span className={styles.archArrow} aria-hidden="true">↓</span>
          <div><b>API Go + worker River</b><span>Auth · master · pesanan · pembayaran · dokumen · laporan</span></div>
          <span className={styles.archArrow} aria-hidden="true">↓</span>
          <div className={styles.archServices}>
            <span>PostgreSQL 18<br />data, sesi, audit, antrean</span>
            <span>MinIO / S3<br />bukti dan dokumen</span>
            <span>Gotenberg<br />render PDF</span>
            <span>SMTP<br />email</span>
            <span>Gemini 2.5 Flash-Lite<br />OCR opsional</span>
          </div>
        </div>
      </Card>
      <nav className={styles.toc} aria-label="Isi dokumentasi teknis">
        <a href="#arsitektur">Arsitektur</a><a href="#relasi">Relasi data</a><a href="#mekanisme">Mekanisme</a><a href="#operasi">Operasi</a>
      </nav>
      <Card as="section" className={styles.article}>
        <h2 id="relasi">Model data dan relasi inti</h2>
        <p>Relasi berikut merangkum tabel utama migrasi produksi. Nominal uang disimpan sebagai BIGINT Rupiah bulat; timestamp disimpan UTC lalu ditampilkan dalam WIB.</p>
        <dl className={styles.entities}>
          {entities.map(([name, explanation]) => <div key={name}><dt>{name}</dt><dd>{explanation}</dd></div>)}
        </dl>
      </Card>
      <Card as="section" className={styles.article}>
        <h2 id="mekanisme">Mekanisme transaksi dan dokumen</h2>
        <div className={styles.techGrid}>
          <div><h3>Status pesanan</h3><p>Draft → submitted. Transfer/Deposit menjadi done setelah nilai penuh terverifikasi. Tempo menjadi approved_unpaid saat disetujui dan done setelah charge lunas. Nomor invoice, serial per baris, tanggal, mode, harga, rekening, dan tanggal bayar disimpan sebagai snapshot saat transaksi yang relevan.</p></div>
          <div><h3>Konsistensi uang</h3><p>Submit, verifikasi, alokasi, revisi, dan jurnal berjalan dalam transaksi PostgreSQL. Idempotency-Key mencegah aksi ekonomi ganda. Pelunasan Tempo dialokasikan FIFO ke charge tertua; kredit Tempo dan jurnal Deposit dipisahkan.</p></div>
          <div><h3>Pekerjaan asinkron</h3><p>River menangani PDF, email, OCR, Surat Tagihan, reminder, ekspor, dan cadangan. Dokumen berversi disimpan di S3; API memberikan tautan unduh berumur pendek. Retry job tidak boleh menggandakan keputusan bisnis.</p></div>
          <div><h3>Kontrol akses</h3><p>Sesi memakai cookie aman dan CSRF; kata sandi di-hash Argon2id. API memeriksa role dan kepemilikan cabang pada setiap permintaan. Rahasia OCR disimpan terenkripsi dan pengelolaannya khusus Superadmin; audit mencatat perubahan tanpa menyalin rahasia.</p></div>
        </div>
      </Card>
      <Card as="section" className={styles.article}>
        <h2 id="operasi">Operasi, pemantauan, dan pemulihan</h2>
        <ul className={styles.steps}>
          <li>Deploy memakai image API/web dan migrasi goose; cek kedua host melalui /readyz dan versi migrasi sebelum membuka trafik.</li>
          <li>Prometheus memantau API, worker, dan layanan pendukung; log JSON dikumpulkan melalui Loki. Endpoint /metrics hanya tersedia di jaringan internal.</li>
          <li>Cadangan database dan objek dienkripsi dengan age, mempunyai manifest, dan restore lokal sudah diuji. Target offsite eksternal serta restore drill dari salinan offsite masih harus disediakan.</li>
          <li>Jika job gagal, periksa status River, log worker, objek S3, dan audit sebelum retry. Jaga nomor, ledger, dan versi dokumen tetap konsisten.</li>
        </ul>
      </Card>
    </>
  );
}

export function DocumentationPage({ section }: { section: Section }) {
  return (
    <>
      <PageHead title="Dokumentasi" sub="Panduan penggunaan dan rancangan teknis sistem Grahita" />
      <div className={styles.stack}>
        <nav className={styles.tabs} aria-label="Jenis dokumentasi">
          <Link to="/dokumentasi" activeOptions={{ exact: true }} aria-current={section === "panduan" ? "page" : undefined}>Panduan Penggunaan</Link>
          <Link to="/dokumentasi/teknis" aria-current={section === "teknis" ? "page" : undefined}>Dokumentasi Teknis</Link>
        </nav>
        {section === "panduan" ? <Guide /> : <Technical />}
      </div>
    </>
  );
}
