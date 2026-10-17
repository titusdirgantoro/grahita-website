import { useState } from "react";

import { useNavigate, useSearch } from "@tanstack/react-router";

import { Button, Card, CardHead, Chip, EmptyState, Notice, Segmented, Select, SkeletonList } from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import type { FilterPesanan } from "./pesanan";
import type { Cabang } from "./shell-pages";
import { bilangan, desimal1, persenPerubahan, rpRingkas, rupiah } from "./dashboard-format";
import { GaugeStatus, GrafikBulanan, GrafikHarian, type BatangBulan } from "./dashboard-grafik";
import styles from "./dashboard.module.css";

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

/** Teks periode terpilih dan pembandingnya, persis PER_TXT/PREV_TXT prototype. */
const TEKS_PERIODE: Record<Periode, string> = { hari: "hari ini", minggu: "minggu ini", bulan: "bulan ini", tahun: "tahun ini" };
const TEKS_SEBELUM: Record<Periode, string> = { hari: "kemarin", minggu: "minggu lalu", bulan: "bulan lalu", tahun: "tahun lalu" };

const BULAN_PANJANG = [
  "Januari", "Februari", "Maret", "April", "Mei", "Juni",
  "Juli", "Agustus", "September", "Oktober", "November", "Desember",
];

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
  terverifikasi: number;
}

export interface PeringkatCabang {
  branch_id: string;
  nama: string;
  jumlah: number;
  nilai?: number;
}

export interface ProdukTerlaris {
  product_id: string;
  nama: string;
  qty: number;
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
  item: number;
  pembanding: { dari: string; sampai: string; ringkasan: Metrik; item: number };
  bulan_lalu: Metrik;
  harian_pembanding: number;
  komposisi: { reguler: number; cnc: number; sertifikat: number; cnc_lewat_tempo: number };
  produk_terlaris: ProdukTerlaris[];
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

/** useFilterDashboard membaca filter dari URL beserta pengubahnya. */
function useFilterDashboard() {
  const search = useSearch({ strict: false }) as FilterDashboard;
  const navigate = useNavigate();
  const cabang = search.cabang ?? "";
  const periode = (PERIODE.some((p) => p.value === search.periode) ? search.periode : PERIODE_AWAL) as Periode;
  const ubah = (patch: FilterDashboard) => void navigate({ to: "/", search: { cabang: cabang || undefined, periode, ...patch } });
  return { cabang, periode, ubah };
}

/** FilterCabangDashboard adalah pemilih cabang di topbar dashboard (kiri). */
export function FilterCabangDashboard() {
  const { cabang, ubah } = useFilterDashboard();
  const { data } = useApi<Cabang[]>("/admin/branches");
  return (
    <Select
      aria-label="Filter cabang"
      inputClassName={styles.cabSelect}
      value={cabang}
      onChange={(e) => ubah({ cabang: e.target.value || undefined })}
    >
      <option value="">Semua cabang</option>
      {(data ?? []).map((c) => (
        <option key={c.id} value={c.id}>
          {c.nama}
        </option>
      ))}
    </Select>
  );
}

/** FilterPeriodeDashboard adalah pemilih periode di topbar dashboard (tengah). */
export function FilterPeriodeDashboard() {
  const { periode, ubah } = useFilterDashboard();
  return <Segmented<Periode> label="Periode" options={PERIODE} value={periode} onChange={(p) => ubah({ periode: p })} />;
}

/** unduhCsv menyimpan ringkasan bulanan sebagai CSV (aksi ekspor prototype). */
function unduhCsv(data: DashboardData, uang: boolean) {
  const tahun = data.filter.hari_ini.slice(0, 4);
  const baris = [["Bulan", uang ? "Nilai pesanan (Rp)" : "Jumlah pesanan", "Terverifikasi"]].concat(
    data.tren_bulanan.map((t, i) => [`${BULAN_PANJANG[i]} ${tahun}`, String(uang ? t.nilai ?? 0 : t.jumlah), String(t.terverifikasi)]),
  );
  const isi = baris.map((r) => r.map((sel) => `"${sel.replace(/"/g, '""')}"`).join(",")).join("\n");
  const url = URL.createObjectURL(new Blob([`﻿${isi}`], { type: "text/csv;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = `ringkasan-bulanan-${tahun}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * pDashboard admin — `/` (admin_v2.html §Dashboard): sapaan + chip "perlu
 * tindakan", penjualan/pesanan per bulan, status pembayaran, pesanan harian,
 * komposisi, cabang teraktif, dan product terlaris (PRD §4.1 AP-DSH-01..06).
 * Filter cabang & periode ada di topbar (FilterCabangDashboard,
 * FilterPeriodeDashboard) dan disimpan di URL.
 *
 * Nilai rupiah TIDAK dihitung di UI: server hanya mengiriminya kepada
 * Superadmin, jadi halaman ini menampilkan apa pun yang benar-benar diterima
 * (`nilai` ada → rupiah, tidak ada → jumlah pesanan).
 */
export function DashboardPage() {
  const { cabang, periode } = useFilterDashboard();
  const navigate = useNavigate();
  const { pengguna } = useSesi();

  const path = `/admin/dashboard?periode=${encodeURIComponent(periode)}` + (cabang ? `&cabang=${encodeURIComponent(cabang)}` : "");
  const { data, memuat, galat, muatUlang } = useApi<DashboardData>(path);

  // Chip yang disembunyikan pengguna HANYA state tampilan (AP-DSH-03), tidak
  // dikirim ke server: menyembunyikan chip tidak mengubah datanya.
  const [disembunyikan, setDisembunyikan] = useState<Record<string, boolean>>({});

  const namaDepan = (pengguna?.nama ?? "").split(" ")[0];
  const perTeks = TEKS_PERIODE[periode];
  const sebelumTeks = TEKS_SEBELUM[periode];

  const chips = (data?.chip ?? []).filter((c) => c.jumlah > 0 && !disembunyikan[c.kunci]);
  const keLaporan = () => void navigate({ to: data?.ringkasan.nilai !== undefined ? "/reporting" : "/pesanan" });
  const keRiwayat = () => void navigate({ to: "/pesanan", search: cabang ? { cabang } : {} });

  return (
    <>
      <section className={styles.greet}>
        <h1>{`Halo${namaDepan ? `, ${namaDepan}` : ""}. Ini ringkasan pesanan cabang ${perTeks}`}</h1>
        <div className={styles.todo}>
          <span className="caption muted">Perlu tindakan</span>
          <div className={styles.chips}>
            <Button
              iconOnly
              icon="plus"
              aria-label="Tampilkan semua chip"
              title="Tampilkan semua chip"
              onClick={() => setDisembunyikan({})}
            />
            {memuat && !data ? (
              <span className="muted small">Memuat…</span>
            ) : chips.length === 0 ? (
              <span className="muted small">Tidak ada. Semua pesanan sudah ditangani.</span>
            ) : (
              chips.map((c) => (
                <Chip
                  key={c.kunci}
                  removeLabel={`Sembunyikan: ${c.jumlah} ${c.label}`}
                  onToggle={() => void navigate({ to: "/pesanan", search: { ...(PADANAN_CHIP[c.kunci] ?? {}), cabang: cabang || undefined } })}
                  onRemove={() => setDisembunyikan((s) => ({ ...s, [c.kunci]: true }))}
                >
                  {`${c.jumlah} ${c.label}`}
                </Chip>
              ))
            )}
          </div>
        </div>
      </section>

      {galat ? (
        <div style={{ marginBottom: 12 }}>
          <Notice tone="danger">
            {galat}{" "}
            <Button variant="secondary" onClick={muatUlang}>
              Coba lagi
            </Button>
          </Notice>
        </div>
      ) : null}

      {!data ? (
        <Card>
          <SkeletonList rows={6} />
        </Card>
      ) : (
        <IsiDashboard
          data={data}
          perTeks={perTeks}
          sebelumTeks={sebelumTeks}
          keLaporan={keLaporan}
          keRiwayat={keRiwayat}
        />
      )}
    </>
  );
}

function IsiDashboard({
  data,
  perTeks,
  sebelumTeks,
  keLaporan,
  keRiwayat,
}: {
  data: DashboardData;
  perTeks: string;
  sebelumTeks: string;
  keLaporan: () => void;
  keRiwayat: () => void;
}) {
  const uang = data.ringkasan.nilai !== undefined;
  const nilai = (m: Metrik) => (uang ? m.nilai ?? 0 : m.jumlah);
  const teksNilai = (v: number) => (uang ? rpRingkas(v) : `${bilangan(v)} pesanan`);
  const judulPeriode = perTeks[0].toUpperCase() + perTeks.slice(1);

  // Kartu 1: per bulan.
  const indeksBulan = Number(data.filter.hari_ini.slice(5, 7)) - 1;
  const tahun = data.filter.hari_ini.slice(0, 4);
  const batang: BatangBulan[] = data.tren_bulanan.map((t, i) => ({
    kunci: t.kunci,
    label: t.label,
    nama: `${BULAN_PANJANG[i]} ${tahun}`,
    nilai: nilai(t),
    keadaan: i < indeksBulan ? "lalu" : i === indeksBulan ? "kini" : "nanti",
  }));
  const perubahan = persenPerubahan(batang[indeksBulan]?.nilai ?? 0, nilai(data.bulan_lalu));
  const gelembung = perubahan === null ? null : `${perubahan >= 0 ? "+" : "−"}${desimal1(Math.abs(perubahan))}%`;
  const vKini = nilai(data.ringkasan);
  const selisih = vKini - nilai(data.pembanding.ringkasan);

  // Kartu 2: status pembayaran.
  const { terverifikasi, menunggu, perlu_review: review } = data.pembayaran;
  const n = data.ringkasan.jumlah || 1;
  const rataNilai = (m: Metrik) => (m.jumlah ? (m.nilai ?? 0) / m.jumlah : 0);
  const rataItemKini = data.ringkasan.jumlah ? data.item / data.ringkasan.jumlah : 0;
  const rataItemSebelum = data.pembanding.ringkasan.jumlah ? data.pembanding.item / data.pembanding.ringkasan.jumlah : 0;
  const dRataNilai = rataNilai(data.ringkasan) - rataNilai(data.pembanding.ringkasan);
  const dRataItem = rataItemKini - rataItemSebelum;

  // Kartu 3: harian bulan ini.
  const [th, bl] = data.filter.hari_ini.split("-").map(Number);
  const hariDalamBulan = new Date(Date.UTC(th, bl, 0)).getUTCDate();
  const harian = Array.from({ length: hariDalamBulan }, (_, i) => data.harian[i]?.jumlah ?? 0);
  const totalBulanIni = data.harian.reduce((s, t) => s + t.jumlah, 0);
  const selisihHarian = totalBulanIni - data.harian_pembanding;

  // Kartu 4: komposisi.
  const k = data.komposisi;
  const totalKomposisi = k.reguler + k.cnc;
  const pReguler = totalKomposisi ? (k.reguler / totalKomposisi) * 100 : 0;
  const pCnc = totalKomposisi ? (k.cnc / totalKomposisi) * 100 : 0;

  // Kartu 5–6: peringkat.
  const maksCabang = data.peringkat_cabang[0]?.jumlah || 1;
  const maksProduk = data.produk_terlaris[0]?.qty || 1;

  const pembanding = (d: number, format: (v: number) => string) => (
    <div className={styles.cmp}>
      {`${d >= 0 ? "Lebih tinggi" : "Lebih rendah"} dari ${sebelumTeks}`}
      <b>{`${d >= 0 ? "+" : "−"}${format(Math.abs(d))}`}</b>
    </div>
  );
  const alat = (
    <>
      <Button variant="secondary" icon="arrow-up-right" onClick={keLaporan}>
        Lihat laporan
      </Button>
      <Button iconOnly icon="sliders-horizontal" aria-label="Filter di riwayat pemesanan" title="Filter" onClick={keRiwayat} />
      <Button iconOnly icon="export" aria-label="Ekspor ringkasan bulanan (CSV)" title="Ekspor" onClick={() => unduhCsv(data, uang)} />
    </>
  );

  return (
    <section className={styles.dash}>
      <Card className={styles.c7}>
        <CardHead title={uang ? "Penjualan per bulan" : "Pesanan per bulan"} tools={alat} />
        <div className={styles.kv}>
          <div>
            <div className={styles.kpi} data-testid="kpi-periode">
              {teksNilai(vKini)}
            </div>
            <div className="muted small">{judulPeriode}</div>
          </div>
          {pembanding(selisih, (v) => (uang ? rpRingkas(v) : bilangan(v)))}
        </div>
        <GrafikBulanan
          batang={batang}
          uang={uang}
          gelembung={gelembung}
          label={uang ? "Penjualan per bulan tahun berjalan" : "Pesanan per bulan tahun berjalan"}
        />
      </Card>

      <Card className={styles.c5}>
        <CardHead
          title="Status pembayaran"
          tools={<Button iconOnly icon="export" aria-label="Ekspor ringkasan bulanan (CSV)" title="Ekspor" onClick={() => unduhCsv(data, uang)} />}
        />
        <div className={styles.gaugeWrap}>
          <div>
            <div className={styles.kpi}>{uang ? rpRingkas(data.ringkasan.nilai ?? 0) : bilangan(data.ringkasan.jumlah)}</div>
            <div className="muted small">{`${uang ? "Nilai pesanan" : "Pesanan"} ${perTeks}`}</div>
            <ul className={styles.legend} aria-label="Rincian status pembayaran">
              <li>
                <span className={styles.sw} style={{ background: "var(--brand-300)" }} />
                Terverifikasi<span className={styles.n}>{terverifikasi.jumlah}</span>
              </li>
              <li>
                <span className={styles.sw} style={{ background: "var(--accent-300)" }} />
                Menunggu<span className={styles.n}>{menunggu.jumlah}</span>
              </li>
              <li>
                <span className={styles.sw} style={{ background: "var(--chart-critical)" }} />
                Perlu review<span className={styles.n}>{review.jumlah}</span>
              </li>
              <li>
                <span className={`${styles.sw} ${styles.swArsir} hatch`} />
                Sisa
              </li>
            </ul>
          </div>
          <GaugeStatus
            cincin={[
              { porsi: terverifikasi.jumlah / n, warna: "var(--brand-300)", judul: `Terverifikasi ${terverifikasi.jumlah}` },
              { porsi: menunggu.jumlah / n, warna: "var(--accent-300)", judul: `Menunggu ${menunggu.jumlah}` },
              { porsi: review.jumlah / n, warna: "var(--chart-critical)", judul: `Perlu review ${review.jumlah}` },
            ]}
            jarum={terverifikasi.jumlah / n}
            labelJarum={uang ? rpRingkas(terverifikasi.nilai ?? 0) : `${terverifikasi.jumlah} pesanan`}
          />
        </div>
        <div className={styles.avg}>
          {uang ? (
            <div>
              <div className="muted small">Rata-rata nilai pesanan</div>
              <div className={styles.v}>
                <span className={styles.kpiSm}>{rpRingkas(rataNilai(data.ringkasan))}</span>
                <span className={styles.pillnote}>{`${rupiah(Math.abs(dRataNilai))} ${dRataNilai >= 0 ? "lebih tinggi" : "lebih rendah"}`}</span>
              </div>
            </div>
          ) : (
            <div>
              <div className="muted small">Pesanan terverifikasi</div>
              <div className={styles.v}>
                <span className={styles.kpiSm}>{terverifikasi.jumlah}</span>
                <span className={styles.pillnote}>{`dari ${data.ringkasan.jumlah}`}</span>
              </div>
            </div>
          )}
          <div>
            <div className="muted small">Rata-rata item per pesanan</div>
            <div className={styles.v}>
              <span className={styles.kpiSm}>{desimal1(rataItemKini)}</span>
              <span className={styles.pillnote}>{`${desimal1(Math.abs(dRataItem))} ${dRataItem >= 0 ? "lebih banyak" : "lebih sedikit"}`}</span>
            </div>
          </div>
        </div>
      </Card>

      <Card className={styles.c7}>
        <CardHead
          title="Pesanan harian bulan ini"
          tools={
            <Button variant="secondary" icon="arrow-up-right" onClick={keRiwayat}>
              Lihat laporan
            </Button>
          }
        />
        <div className={styles.kv}>
          <div>
            <div className={styles.kpi}>{`${bilangan(totalBulanIni)} pesanan`}</div>
            <div className="muted small">{`${BULAN_PANJANG[bl - 1]} ${th}`}</div>
          </div>
          <div className={styles.cmp}>
            Dibanding periode sama bulan lalu
            <b>{`${selisihHarian >= 0 ? "+" : "−"}${bilangan(Math.abs(selisihHarian))}`}</b>
          </div>
        </div>
        <GrafikHarian nilai={harian} indeksHariIni={data.harian.length - 1} bulan={String(bl).padStart(2, "0")} />
      </Card>

      <Card className={styles.c5}>
        <CardHead
          title="Komposisi pesanan"
          tools={
            <Button variant="secondary" icon="arrow-up-right" onClick={keRiwayat}>
              Lihat laporan
            </Button>
          }
        />
        {totalKomposisi === 0 ? <p className="muted small">{`Belum ada pesanan ${perTeks}.`}</p> : null}
        <div className={styles.pills2}>
          <div>
            <div className={styles.pcHead}>
              <span className="muted small">Reguler</span>
              <span className={styles.v}>
                <span className={styles.kpiSm}>{k.reguler}</span>
                <span className={styles.pillnote}>{`${k.sertifikat} sertifikat`}</span>
              </span>
            </div>
            <div className={`${styles.pcol} hatch`}>
              {k.reguler ? (
                <div className={styles.fill} style={{ height: `${Math.max(pReguler, 24)}%`, background: "var(--brand-300)" }}>
                  <span className={styles.flabel}>{`${desimal1(pReguler)}%`}</span>
                </div>
              ) : null}
            </div>
          </div>
          <div>
            <div className={styles.pcHead}>
              <span className="muted small">Cash and Carry</span>
              <span className={styles.v}>
                <span className={styles.kpiSm}>{k.cnc}</span>
                <span className={styles.pillnote}>{`${k.cnc_lewat_tempo} lewat tempo`}</span>
              </span>
            </div>
            <div className={`${styles.pcol} hatch`}>
              {k.cnc ? (
                <div className={styles.fill} style={{ height: `${Math.max(pCnc, 24)}%`, background: "var(--accent-300)" }}>
                  <span className={styles.flabel}>{`${desimal1(pCnc)}%`}</span>
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </Card>

      <Card className={styles.c6}>
        <CardHead title="Cabang teraktif" tools={<span className="muted small">{perTeks}</span>} />
        {data.peringkat_cabang.length ? (
          <div className={styles.rank} data-testid="cabang-teraktif">
            {data.peringkat_cabang.map((p) => (
              <div key={p.branch_id} className={styles.row}>
                <span className={styles.nm}>{p.nama}</span>
                <span className="muted small tabular">{`${p.jumlah} pesanan${p.nilai !== undefined ? ` · ${rpRingkas(p.nilai)}` : ""}`}</span>
                <span className={`${styles.trk} hatch`}>
                  <span style={{ width: `${(p.jumlah / maksCabang) * 100}%` }} />
                </span>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState icon="storefront">{`Belum ada pesanan ${perTeks}.`}</EmptyState>
        )}
      </Card>

      <Card className={styles.c6}>
        <CardHead title="Product terlaris" tools={<span className="muted small">dalam qty</span>} />
        {data.produk_terlaris.length ? (
          <div className={styles.rank} data-testid="produk-terlaris">
            {data.produk_terlaris.map((p) => (
              <div key={p.product_id} className={styles.row}>
                <span className={styles.nm}>{p.nama}</span>
                <span className="muted small tabular">{`${bilangan(p.qty)} item`}</span>
                <span className={`${styles.trk} hatch`}>
                  <span style={{ width: `${(p.qty / maksProduk) * 100}%` }} />
                </span>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState icon="tag">{`Belum ada product terjual ${perTeks}.`}</EmptyState>
        )}
      </Card>
    </section>
  );
}
