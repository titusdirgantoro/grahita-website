import { useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";

import {
  Badge,
  Button,
  Card,
  CardHead,
  EmptyState,
  Notice,
  PageHead,
  SkeletonList,
  Table,
} from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import type { Pesanan, StatusPesanan } from "./pesanan";
import styles from "./beranda.module.css";

/**
 * pBeranda — / (PRD §3.3; kartu F1-19).
 *
 * Isi: ringkasan periode berjalan (jumlah pesanan, total belanja, yang sudah
 * selesai), pesanan Submitted yang menunggu verifikasi, draft yang belum
 * dikirim, dan 5 pesanan terakhir — plus pintasan "Buat pesanan".
 *
 * Seluruh angka dihitung dari GET /api/v1/orders, satu-satunya sumber daftar
 * pesanan cabang yang sudah dibatasi branch_id SESI di server (isolasi data
 * cabang). Tidak ada endpoint agregat baru, jadi angka di beranda, halaman
 * Pesanan, dan riwayat invoice selalu berasal dari data DB yang sama.
 */

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

/** hariIni mengembalikan tanggal bisnis WIB (UTC+7) sebagai YYYY-MM-DD. */
export function hariIni(sekarang: Date = new Date()): string {
  // Memakai WIB, BUKAN zona waktu browser: orders.tanggal adalah tanggal bisnis
  // WIB (BR-04), sehingga "periode berjalan" tidak bergeser
  // bagi pengguna yang membuka portal dari zona waktu lain dekat pergantian
  // hari/bulan. WIB tidak punya DST, jadi geserannya tetap +7 jam.
  const wib = new Date(sekarang.getTime() + 7 * 60 * 60 * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${wib.getUTCFullYear()}-${p(wib.getUTCMonth() + 1)}-${p(wib.getUTCDate())}`;
}

/** bulanDari mengambil YYYY-MM dari tanggal bisnis (YYYY-MM-DD). */
export function bulanDari(tanggal?: string): string {
  return tanggal ? tanggal.slice(0, 7) : "";
}

/** bulanSebelumnya menggeser YYYY-MM satu bulan ke belakang. */
export function bulanSebelumnya(bulan: string): string {
  const [tahun, bulanKe] = bulan.split("-").map(Number);
  const d = new Date(Date.UTC(tahun, bulanKe - 2, 1));
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}`;
}

/** tanggalPanjang menampilkan tanggal dalam bentuk "09 Oktober 2026". */
function tanggalPanjang(tanggal: string): string {
  const d = new Date(`${tanggal}T00:00:00`);
  if (Number.isNaN(d.getTime())) return tanggal;
  return d.toLocaleDateString("id-ID", { day: "2-digit", month: "long", year: "numeric" });
}

/** tanggalRingkas menampilkan tanggal bisnis pesanan (WIB) tanpa menggeser hari. */
function tanggalRingkas(tanggal?: string): string {
  if (!tanggal) return "—";
  const d = new Date(`${tanggal}T00:00:00`);
  if (Number.isNaN(d.getTime())) return tanggal;
  return d.toLocaleDateString("id-ID", { day: "2-digit", month: "short", year: "numeric" });
}

/** masukPeriode melaporkan apakah tanggal bisnis berada di bulan tertentu. */
function masukPeriode(tanggal: string | undefined, bulan: string): boolean {
  return bulanDari(tanggal) === bulan;
}

/** labelStatus adalah teks status pesanan pada tabel beranda. */
function labelStatus(status: StatusPesanan, verif?: string): string {
  if (status === "draft") return "Draft";
  if (status === "done") return "Selesai";
  return verif === "perlu_review" ? "Perlu review" : "Menunggu verifikasi";
}

function toneStatus(status: StatusPesanan, verif?: string): "draft" | "warning" | "success" | "danger" {
  if (status === "draft") return "draft";
  if (status === "done") return "success";
  return verif === "perlu_review" ? "danger" : "warning";
}

export function BerandaPage() {
  const navigate = useNavigate();
  const { pengguna } = useSesi();
  const { data, memuat, galat } = useApi<Pesanan[]>("/orders");

  const hariIniStr = hariIni();
  const bulan = bulanDari(hariIniStr);
  const bulanLalu = bulanSebelumnya(bulan);

  const r = useMemo(() => {
    const semua = data ?? [];
    const nonDraft = semua.filter((o) => o.status !== "draft");
    const periodeIni = nonDraft.filter((o) => masukPeriode(o.tanggal, bulan));
    const periodeLalu = nonDraft.filter((o) => masukPeriode(o.tanggal, bulanLalu));
    const selesai = periodeIni.filter((o) => o.status === "done");
    const totalIni = periodeIni.reduce((s, o) => s + o.total, 0);
    const totalLalu = periodeLalu.reduce((s, o) => s + o.total, 0);
    const selisihJumlah = periodeIni.length - periodeLalu.length;
    const persen = totalLalu > 0 ? Math.round(((totalIni - totalLalu) / totalLalu) * 100) : 0;
    const menunggu = semua.filter((o) => o.status === "submitted");
    const draft = semua.filter((o) => o.status === "draft");
    // 5 pesanan terakhir mengikuti urutan server apa adanya: ListOrdersForBranch
    // sudah mengurutkan COALESCE(tanggal, created_at) DESC, sehingga klien tidak
    // perlu menyortir ulang dengan kunci campuran yang bisa salah urut.
    const terakhir = semua.slice(0, 5);
    return {
      periodeIni,
      selesai,
      totalIni,
      selisihJumlah,
      persen,
      persenSelesai: periodeIni.length > 0 ? Math.round((selesai.length / periodeIni.length) * 100) : 0,
      menunggu,
      draft,
      terakhir,
    };
  }, [data, bulan, bulanLalu]);

  return (
    <>
      <PageHead
        title="Beranda"
        sub={
          pengguna
            ? `${pengguna.nama} · ${tanggalPanjang(hariIniStr)}`
            : tanggalPanjang(hariIniStr)
        }
        actions={
          <Button variant="primary" icon="plus" onClick={() => void navigate({ to: "/pesanan/baru" })}>
            Buat pesanan
          </Button>
        }
      />

      {memuat ? (
        <Card>
          <SkeletonList rows={3} />
        </Card>
      ) : galat ? (
        <Notice tone="danger">{galat}</Notice>
      ) : (
        <div className={styles.stack}>
          <section className={styles.kpis} aria-label="Ringkasan periode berjalan">
            <Card>
              <div className={styles.kpi}>
                <div>
                  <p className={styles.lbl}>Pesanan bulan ini</p>
                  <p className={`${styles.val} tabular`} data-testid="kpi-pesanan-periode">
                    {r.periodeIni.length}
                  </p>
                  <span className="caption">
                    {r.selisihJumlah === 0
                      ? "Sama dengan bulan lalu"
                      : `${Math.abs(r.selisihJumlah)} ${r.selisihJumlah > 0 ? "lebih banyak" : "lebih sedikit"} dari bulan lalu`}
                  </span>
                </div>
              </div>
            </Card>
            <Card>
              <div className={styles.kpi}>
                <div>
                  <p className={styles.lbl}>Total belanja bulan ini</p>
                  <p className={`${styles.val} tabular`} data-testid="kpi-total-periode">
                    {rupiah.format(r.totalIni)}
                  </p>
                  <span className="caption">
                    {r.persen === 0 ? "Sama dengan bulan lalu" : `${Math.abs(r.persen)}% dari bulan lalu`}
                  </span>
                </div>
              </div>
            </Card>
            <Card>
              <div className={styles.kpi}>
                <div>
                  <p className={styles.lbl}>Menunggu verifikasi</p>
                  <p className={`${styles.val} tabular`} data-testid="kpi-menunggu">
                    {r.menunggu.length}
                  </p>
                  <span className="caption">{r.selesai.length} terverifikasi bulan ini</span>
                </div>
              </div>
            </Card>
          </section>

          <section className={styles.dua}>
            <Card>
              <CardHead title="Pesanan selesai" />
              <p className={styles.gauge}>{r.persenSelesai}%</p>
              <p className="muted small">
                {r.selesai.length} dari {r.periodeIni.length} pesanan bulan ini sudah selesai.
              </p>
            </Card>
            <Card>
              <CardHead
                title="Draft belum dikirim"
                tools={
                  r.draft.length > 0 ? (
                    <Button
                      variant="ghost"
                      iconOnly
                      icon="arrow-up-right"
                      aria-label="Lihat draft"
                      onClick={() => void navigate({ to: "/pesanan", search: { status: "draft" } })}
                    />
                  ) : undefined
                }
              />
              <p className={`${styles.val} tabular`} data-testid="kpi-draft">
                {r.draft.length}
              </p>
              <p className="muted small">
                {r.draft.length === 0
                  ? "Tidak ada draft. Semua pesanan sudah dikirim atau dihapus."
                  : "Lanjutkan dan kirim draft agar diproses Pusat."}
              </p>
            </Card>
          </section>

          <section className={styles.banners}>
            <button
              type="button"
              className={`${styles.banner} ${styles.dark}`}
              onClick={() => void navigate({ to: "/pesanan", search: { status: "submitted" } })}
            >
              <span className={styles.ring}>{r.menunggu.length}</span>
              <span>
                <span className={styles.ttl}>
                  {r.menunggu.length > 0
                    ? "Pesanan menunggu verifikasi"
                    : "Semua pembayaran sudah terverifikasi"}
                </span>
                <br />
                <span className={styles.sub}>
                  {r.menunggu.length > 0
                    ? "Admin Pusat sedang memeriksa bukti transfer Anda."
                    : "Invoice tersedia di Riwayat invoice setelah pesanan selesai."}
                </span>
              </span>
            </button>

            <button
              type="button"
              className={styles.banner}
              onClick={() => void navigate({ to: "/pesanan", search: { status: "draft" } })}
            >
              <span className={styles.ring}>{r.draft.length}</span>
              <span>
                <span className={styles.ttl}>{r.draft.length} draft belum dikirim</span>
                <br />
                <span className={styles.sub}>Lanjutkan dan kirim agar diproses Pusat.</span>
              </span>
            </button>

            <button
              type="button"
              className={`${styles.banner} ${styles.guide}`}
              onClick={() => void navigate({ to: "/tutorial" })}
            >
              <span>
                <span className={styles.ttl}>Cara membuat pesanan</span>
                <br />
                <span className={styles.sub}>
                  Dari memilih produk sampai mengunggah bukti transfer, dalam 6 langkah.
                </span>
              </span>
            </button>
          </section>

          <Card>
            <CardHead
              title="Pesanan terakhir"
              tools={
                <Button variant="ghost" onClick={() => void navigate({ to: "/pesanan" })}>
                  Semua
                </Button>
              }
            />
            {r.terakhir.length === 0 ? (
              <EmptyState
                icon="receipt"
                action={
                  <Button variant="secondary" icon="plus" onClick={() => void navigate({ to: "/pesanan/baru" })}>
                    Buat pesanan pertama
                  </Button>
                }
              >
                Belum ada pesanan. Pesanan yang Anda kirim akan tampil di sini beserta statusnya.
              </EmptyState>
            ) : (
              <Table mobileCards caption="Lima pesanan terakhir cabang">
                <thead>
                  <tr>
                    <th scope="col">Nomor</th>
                    <th scope="col">Tanggal</th>
                    <th scope="col" className="num">
                      Total
                    </th>
                    <th scope="col">Status</th>
                  </tr>
                </thead>
                <tbody>
                  {r.terakhir.map((o) => (
                    <tr key={o.id}>
                      <td>
                        <b className="tabular">{o.nomor ?? "Draft"}</b>
                        {o.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
                      </td>
                      <td className="tabular muted">{tanggalRingkas(o.tanggal)}</td>
                      <td className="num">
                        <b>{rupiah.format(o.total)}</b>
                      </td>
                      <td>
                        <Badge tone={toneStatus(o.status, o.verif_status)}>
                          {labelStatus(o.status, o.verif_status)}
                        </Badge>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </div>
      )}
    </>
  );
}
