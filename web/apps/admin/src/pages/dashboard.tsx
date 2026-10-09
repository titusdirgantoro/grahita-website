import { useState } from "react";

import { Link, useNavigate, useSearch } from "@tanstack/react-router";

import {
  Badge,
  Button,
  Card,
  CardHead,
  Chip,
  EmptyState,
  Field,
  Notice,
  PageHead,
  Segmented,
  Select,
  SkeletonList,
} from "@grahita/ui";

import { useApi } from "../useApi";
import type { FilterPesanan } from "./pesanan";
import type { Cabang } from "./shell-pages";
import styles from "./admin-pages.module.css";

/**
 * FilterDashboard adalah state filter dashboard admin yang disimpan di URL
 * search params (SystemDesign.md §7.2) supaya bisa di-bookmark/di-share.
 * Nilainya dipetakan satu-satu ke query string GET /api/v1/admin/dashboard.
 */
export interface FilterDashboard {
  cabang?: string;
  periode?: string;
}

/** Periode yang dikenal server (PRD AP-DSH-01). */
export type Periode = "hari" | "minggu" | "bulan" | "tahun";

const PERIODE: ReadonlyArray<{ value: Periode; label: string }> = [
  { value: "hari", label: "Hari ini" },
  { value: "minggu", label: "Minggu ini" },
  { value: "bulan", label: "Bulan ini" },
  { value: "tahun", label: "Tahun ini" },
];

const PERIODE_AWAL: Periode = "bulan";

/** Metrik berbasis role: `nilai` hanya ada untuk Superadmin (AP-DSH-02). */
export interface Metrik {
  jumlah: number;
  nilai?: number;
}

export interface ChipTindakan {
  kunci: string;
  label: string;
  jumlah: number;
}

export interface TitikTren {
  kunci: string;
  label: string;
  jumlah: number;
  nilai?: number;
}

export interface PeringkatCabang {
  branch_id: string;
  nama: string;
  jumlah: number;
  nilai?: number;
}

export interface DashboardData {
  filter: {
    cabang?: string;
    periode: Periode;
    dari: string;
    sampai: string;
    hari_ini: string;
    zona: string;
  };
  ringkasan: Metrik;
  pembayaran: { terverifikasi: Metrik; menunggu: Metrik; perlu_review: Metrik };
  chip: ChipTindakan[];
  tren_bulanan: TitikTren[];
  harian: TitikTren[];
  peringkat_cabang: PeringkatCabang[];
}

/**
 * PADANAN_CHIP memetakan tiap chip "perlu tindakan" ke filter riwayat
 * pemesanan yang MEMBUKTIKAN angkanya (PRD AP-DSH-03). Padanan ini persis sama
 * dengan klausa hitung di server, jadi "3 menunggu verifikasi" selalu berarti
 * 3 baris yang muncul saat chip diklik.
 */
export const PADANAN_CHIP: Record<string, FilterPesanan> = {
  menunggu: { status: "submitted", verif: "belum" },
  perlu_review: { verif: "perlu_review" },
  lewat_jatuh_tempo: { terlambat: true },
  email_gagal: { email_gagal: true },
};

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});
const angka = new Intl.NumberFormat("id-ID");

/** teksMetrik menampilkan metrik sesuai role: rupiah atau jumlah pesanan. */
function teksMetrik(m: Metrik): string {
  return m.nilai === undefined ? `${angka.format(m.jumlah)} pesanan` : rupiah.format(m.nilai);
}

/**
 * GrafikBatang adalah grafik SVG buatan sendiri (SystemDesign.md §7.2, tidak
 * ada library chart): responsif lewat `viewBox` + lebar 100%, dan aksesibel
 * lewat `role="img"`, `aria-label`, `<title>`/`<desc>`, serta `<title>` per
 * batang untuk pembaca layar dan tooltip bawaan browser.
 */
function GrafikBatang({
  titik,
  ariaLabel,
  deskripsi,
  nilaiTeks,
}: {
  titik: TitikTren[];
  ariaLabel: string;
  deskripsi: string;
  nilaiTeks: (t: TitikTren) => string;
}) {
  const lebarBatang = 22;
  const celah = 8;
  const tinggi = 96;
  const ruangLabel = 18;
  const maks = Math.max(...titik.map((t) => t.jumlah), 1);
  const lebarTotal = Math.max(titik.length * (lebarBatang + celah) - celah, 1);
  return (
    <div className={styles.chartWrap}>
      <svg
        className={styles.chart}
        viewBox={`0 0 ${lebarTotal} ${tinggi + ruangLabel}`}
        width="100%"
        height={tinggi + ruangLabel}
        role="img"
        aria-label={ariaLabel}
        preserveAspectRatio="xMidYMid meet"
      >
        <title>{ariaLabel}</title>
        <desc>{deskripsi}</desc>
        {titik.map((t, i) => {
          const h = t.jumlah > 0 ? Math.max(3, (tinggi * t.jumlah) / maks) : 3;
          const x = i * (lebarBatang + celah);
          return (
            <g key={t.kunci}>
              <rect
                x={x}
                y={tinggi - h}
                width={lebarBatang}
                height={h}
                rx={3}
                className={t.jumlah > 0 ? styles.batang : styles.batangKosong}
              >
                <title>{`${t.label}: ${nilaiTeks(t)}`}</title>
              </rect>
              <text x={x + lebarBatang / 2} y={tinggi + 13} textAnchor="middle" className={styles.sumbu}>
                {t.label}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

/**
 * pDashboard admin — `/`: filter cabang + periode, chip "perlu tindakan",
 * metrik berbasis role, grafik SVG, ringkasan pembayaran, dan peringkat cabang
 * (PRD §4.1 AP-DSH-01..06).
 *
 * Nilai rupiah TIDAK dihitung di UI: server hanya mengiriminya kepada
 * Superadmin, jadi halaman ini menampilkan apa pun yang benar-benar diterima
 * (`nilai` ada → rupiah, tidak ada → jumlah pesanan).
 */
export function DashboardPage() {
  const search = useSearch({ strict: false }) as FilterDashboard;
  const navigate = useNavigate();
  const cabang = search.cabang ?? "";
  const periode = (search.periode as Periode | undefined) ?? PERIODE_AWAL;

  const { data: daftarCabang } = useApi<Cabang[]>("/admin/branches");
  const path = `/admin/dashboard?periode=${encodeURIComponent(periode)}` +
    (cabang ? `&cabang=${encodeURIComponent(cabang)}` : "");
  const { data, memuat, galat, muatUlang } = useApi<DashboardData>(path);

  // Chip yang disembunyikan pengguna HANYA state tampilan (AP-DSH-03), tidak
  // dikirim ke server: menyembunyikan chip tidak mengubah datanya.
  const [disembunyikan, setDisembunyikan] = useState<Record<string, boolean>>({});

  function ubahFilter(patch: FilterDashboard) {
    void navigate({ to: "/", search: { cabang, periode, ...patch } });
  }

  const chips = (data?.chip ?? []).filter((c) => c.jumlah > 0 && !disembunyikan[c.kunci]);
  const adaTersembunyi = (data?.chip ?? []).some((c) => c.jumlah > 0 && disembunyikan[c.kunci]);
  const nilaiAktif = data?.ringkasan.nilai !== undefined;

  return (
    <>
      <PageHead
        title="Dashboard"
        sub={
          data
            ? `Ringkasan ${periode} · ${data.filter.dari} s/d ${data.filter.sampai} (${data.filter.zona})`
            : "Ringkasan seluruh cabang"
        }
      />

      {galat ? (
        <Notice tone="danger">
          {galat}{" "}
          <Button variant="secondary" onClick={muatUlang}>
            Coba lagi
          </Button>
        </Notice>
      ) : null}

      <div className={styles.stack}>
        <Card>
          <CardHead
            title="Filter"
            tools={
              <Segmented<Periode>
                label="Periode"
                options={PERIODE}
                value={periode}
                onChange={(p) => ubahFilter({ periode: p })}
              />
            }
          />
          <div className={styles.row}>
            <Field label="Cabang" htmlFor="dash-cabang">
              <Select
                id="dash-cabang"
                value={cabang}
                onChange={(e) => ubahFilter({ cabang: e.target.value })}
              >
                <option value="">Semua cabang</option>
                {(daftarCabang ?? []).map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.nama}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
        </Card>

        <Card>
          <CardHead
            title="Perlu tindakan"
            tools={
              <Button
                iconOnly
                icon="plus"
                aria-label="Tampilkan semua chip"
                title="Tampilkan semua chip"
                disabled={!adaTersembunyi}
                onClick={() => setDisembunyikan({})}
              />
            }
          />
          {memuat && !data ? (
            <SkeletonList rows={1} />
          ) : chips.length === 0 ? (
            <p className="muted small">Tidak ada. Semua pesanan sudah ditangani.</p>
          ) : (
            <div className={styles.chips}>
              {chips.map((c) => (
                <Chip
                  key={c.kunci}
                  removeLabel={`Sembunyikan: ${c.jumlah} ${c.label}`}
                  onToggle={() =>
                    void navigate({ to: "/pesanan", search: PADANAN_CHIP[c.kunci] ?? {} })
                  }
                  onRemove={() => setDisembunyikan((s) => ({ ...s, [c.kunci]: true }))}
                >
                  {`${c.jumlah} ${c.label}`}
                </Chip>
              ))}
            </div>
          )}
        </Card>

        <div className={styles.kpis}>
          <Card>
            <p className={styles.lbl}>
              {nilaiAktif ? "Nilai pesanan" : "Jumlah pesanan"} {periode} ini
            </p>
            <p className={styles.val}>{data ? teksMetrik(data.ringkasan) : "…"}</p>
            <span className="caption">
              {nilaiAktif ? "Dilihat sebagai nilai rupiah (Superadmin)" : "Dilihat sebagai jumlah pesanan"}
            </span>
          </Card>
          <Card>
            <p className={styles.lbl}>Status pembayaran</p>
            {data ? (
              <div className={styles.row}>
                <Badge tone="success">{`${data.pembayaran.terverifikasi.jumlah} terverifikasi`}</Badge>
                <Badge tone="warning">{`${data.pembayaran.menunggu.jumlah} menunggu`}</Badge>
                <Badge tone="danger">{`${data.pembayaran.perlu_review.jumlah} perlu review`}</Badge>
              </div>
            ) : (
              <p className={styles.val}>…</p>
            )}
            <span className="caption">Pesanan pada periode terpilih</span>
          </Card>
          <Card>
            <p className={styles.lbl}>Cabang aktif</p>
            <p className={styles.val}>
              {daftarCabang ? daftarCabang.filter((c) => c.is_active).length : "…"}
            </p>
            <span className="caption">Dari master cabang</span>
          </Card>
        </div>

        <div className={styles.grid2}>
          <Card>
            <CardHead title={nilaiAktif ? "Penjualan per bulan" : "Pesanan per bulan"} />
            {data ? (
              <GrafikBatang
                titik={data.tren_bulanan}
                ariaLabel="Tren 12 bulan tahun berjalan"
                deskripsi={`Jumlah pesanan per bulan tahun ${data.filter.hari_ini.slice(0, 4)}, dari Januari sampai Desember`}
                nilaiTeks={(t) => teksMetrik(t)}
              />
            ) : (
              <SkeletonList rows={1} />
            )}
          </Card>
          <Card>
            <CardHead title="Harian bulan ini" />
            {data ? (
              <GrafikBatang
                titik={data.harian}
                ariaLabel="Tren harian bulan berjalan"
                deskripsi={`Jumlah pesanan per hari pada bulan ${data.filter.hari_ini.slice(0, 7)}`}
                nilaiTeks={(t) => teksMetrik(t)}
              />
            ) : (
              <SkeletonList rows={1} />
            )}
          </Card>
        </div>

        <Card>
          <CardHead title="Peringkat cabang" tools={<Badge tone="neutral">{periode}</Badge>} />
          {data && data.peringkat_cabang.length > 0 ? (
            <ol className={styles.peringkat}>
              {data.peringkat_cabang.map((p, i) => (
                <li key={p.branch_id}>
                  <span className={styles.peringkatNo}>{i + 1}</span>
                  <span className={styles.peringkatNama}>{p.nama}</span>
                  <span className={styles.peringkatNilai}>{teksMetrik(p)}</span>
                </li>
              ))}
            </ol>
          ) : (
            <EmptyState icon="chart-line">Belum ada pesanan pada periode ini.</EmptyState>
          )}
        </Card>

        <p className="muted small">
          Semua tanggal bisnis memakai zona {data?.filter.zona ?? "Asia/Jakarta"}. Chip hanya menghitung
          pesanan pada cabang terpilih, bukan periode — klik chip untuk membuka daftar terfilternya.{" "}
          <Link to="/pesanan" className={styles.tautanNomor}>
            Riwayat pemesanan
          </Link>
        </p>
      </div>
    </>
  );
}
