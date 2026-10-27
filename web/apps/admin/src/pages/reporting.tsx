import { useEffect, useMemo, useState, type FormEvent } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import { Button, Card, EmptyState, Field, Input, Notice, PageHead, Select, SkeletonList } from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import { PenjagaSuperadmin, type Cabang } from "./shell-pages";
import styles from "./reporting.module.css";

interface FilterLaporan {
  dari: string;
  sampai: string;
  cabang: string;
  status: string;
}

interface BarisLaporan {
  tanggal: string;
  nomor: string;
  versi: number;
  cabang: string;
  line_no: number;
  produk: string;
  nama_instansi: string;
  serial_number: string;
  qty: number;
  subtotal: number;
  status: string;
}

interface HalamanLaporan {
  items: BarisLaporan[];
  total: number;
  total_subtotal: number;
  halaman: number;
  per_halaman: number;
}

interface PekerjaanEkspor {
  id: string;
  status: "queued" | "proses" | "selesai" | "gagal";
  nama_file?: string;
  jumlah_baris: number;
  pesan?: string;
}

const kosong: FilterLaporan = { dari: "", sampai: "", cabang: "", status: "" };
const uang = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

function pesan(err: unknown) {
  return err instanceof ApiClientError ? err.message : "Permintaan belum berhasil. Coba lagi.";
}

export function ReportingPage() {
  const { peran } = useSesi();
  const aktif = peran === "superadmin";
  const [form, setForm] = useState<FilterLaporan>(kosong);
  const [filter, setFilter] = useState<FilterLaporan>(kosong);
  const [halaman, setHalaman] = useState(1);
  const [pekerjaan, setPekerjaan] = useState<PekerjaanEkspor | null>(null);
  const [sibukEkspor, setSibukEkspor] = useState(false);
  const [galatEkspor, setGalatEkspor] = useState<string | null>(null);
  const cabang = useApi<Cabang[]>("/admin/branches", aktif);

  const path = useMemo(() => {
    const q = new URLSearchParams({ halaman: String(halaman), per_halaman: "25" });
    if (filter.dari) q.set("dari", filter.dari);
    if (filter.sampai) q.set("sampai", filter.sampai);
    if (filter.cabang) q.set("cabang", filter.cabang);
    if (filter.status) q.set("status", filter.status);
    return `/admin/reports/order-lines?${q.toString()}`;
  }, [filter, halaman]);
  const laporan = useApi<HalamanLaporan>(path, aktif);

  useEffect(() => {
    if (!pekerjaan || pekerjaan.status === "selesai" || pekerjaan.status === "gagal") return;
    let hidup = true;
    const timer = window.setTimeout(() => {
      void api.get<PekerjaanEkspor>(`/admin/reports/export/${pekerjaan.id}`)
        .then((hasil) => { if (hidup) setPekerjaan(hasil); })
        .catch((err: unknown) => {
          if (hidup) {
            setGalatEkspor(pesan(err));
            setPekerjaan(null);
          }
        });
    }, 2000);
    return () => { hidup = false; window.clearTimeout(timer); };
  }, [pekerjaan]);

  function terapkan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setHalaman(1);
    setFilter({ ...form });
    setPekerjaan(null);
    setGalatEkspor(null);
  }

  async function mulaiEkspor() {
    setSibukEkspor(true);
    setGalatEkspor(null);
    try {
      const hasil = await api.post<PekerjaanEkspor>("/admin/reports/export", {
        dari: filter.dari,
        sampai: filter.sampai,
        cabang: filter.cabang ? [filter.cabang] : [],
        status: filter.status,
      });
      setPekerjaan(hasil);
    } catch (err) {
      setGalatEkspor(pesan(err));
    } finally {
      setSibukEkspor(false);
    }
  }

  async function unduhEkspor() {
    if (!pekerjaan) return;
    setGalatEkspor(null);
    try {
      const hasil = await api.get<{ url: string }>(`/admin/reports/export/${pekerjaan.id}/download`);
      window.open(hasil.url, "_blank", "noopener,noreferrer");
    } catch (err) {
      setGalatEkspor(pesan(err));
    }
  }

  const jumlahHalaman = Math.max(1, Math.ceil((laporan.data?.total ?? 0) / (laporan.data?.per_halaman || 25)));

  return (
    <PenjagaSuperadmin>
      <PageHead title="Reporting" sub="Satu baris per item pesanan, termasuk nama instansinya" />
      <div className={styles.stack}>
        <Card>
          <form className={styles.filters} onSubmit={terapkan}>
            <Field label="Dari tanggal" htmlFor="laporan-dari">
              <Input id="laporan-dari" type="date" value={form.dari} max={form.sampai || undefined}
                onChange={(e) => setForm((lama) => ({ ...lama, dari: e.target.value }))} />
            </Field>
            <Field label="Sampai tanggal" htmlFor="laporan-sampai">
              <Input id="laporan-sampai" type="date" value={form.sampai} min={form.dari || undefined}
                onChange={(e) => setForm((lama) => ({ ...lama, sampai: e.target.value }))} />
            </Field>
            <Field label="Cabang" htmlFor="laporan-cabang">
              <Select id="laporan-cabang" value={form.cabang}
                onChange={(e) => setForm((lama) => ({ ...lama, cabang: e.target.value }))}>
                <option value="">Semua cabang</option>
                {(cabang.data ?? []).map((c) => <option key={c.id} value={c.id}>{c.nama}</option>)}
              </Select>
            </Field>
            <Field label="Status" htmlFor="laporan-status">
              <Select id="laporan-status" value={form.status}
                onChange={(e) => setForm((lama) => ({ ...lama, status: e.target.value }))}>
                <option value="">Semua status</option>
                <option value="submitted">Dikirim</option>
                <option value="approved_unpaid">Disetujui, belum lunas</option>
                <option value="done">Selesai</option>
              </Select>
            </Field>
            <Button type="submit" variant="secondary">Terapkan filter</Button>
          </form>
        </Card>
        <Card>
          <div className={styles.toolbar}>
            <div>
              <strong>{laporan.data?.total ?? 0} baris</strong>
              <span className={styles.muted}> · Total {uang.format(laporan.data?.total_subtotal ?? 0)}</span>
            </div>
            <Button variant="secondary" icon="download-simple" onClick={() => void mulaiEkspor()}
              disabled={sibukEkspor || pekerjaan?.status === "queued" || pekerjaan?.status === "proses"}>
              {sibukEkspor ? "Menyiapkan…" : "Ekspor Excel"}
            </Button>
          </div>
          {galatEkspor ? <Notice tone="danger">{galatEkspor}</Notice> : null}
          {pekerjaan?.status === "queued" || pekerjaan?.status === "proses"
            ? <Notice tone="info">Ekspor sedang diproses. Berkas akan tersedia di sini.</Notice> : null}
          {pekerjaan?.status === "gagal" ? <Notice tone="danger">{pekerjaan.pesan || "Ekspor gagal."}</Notice> : null}
          {pekerjaan?.status === "selesai" ? (
            <div className={styles.download}>
              <span>{pekerjaan.jumlah_baris} baris siap diunduh.</span>
              <Button variant="primary" icon="download-simple" onClick={() => void unduhEkspor()}>Unduh Excel</Button>
            </div>
          ) : null}
          {laporan.galat ? <Notice tone="danger">{laporan.galat}</Notice> : null}
          {laporan.memuat ? <SkeletonList rows={5} /> : laporan.data?.items.length ? (
            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <thead><tr>
                  <th scope="col">Tanggal</th><th scope="col">Invoice</th><th scope="col">Cabang</th>
                  <th scope="col">Nama instansi</th><th scope="col">Produk</th><th scope="col">Serial number</th>
                  <th scope="col" className={styles.num}>Qty</th><th scope="col" className={styles.num}>Subtotal</th><th scope="col">Status</th>
                </tr></thead>
                <tbody>{laporan.data.items.map((baris) => (
                  <tr key={`${baris.nomor}-${baris.versi}-${baris.line_no}`}>
                    <td>{baris.tanggal}</td><td>{baris.nomor}{baris.versi ? `-R${baris.versi}` : ""}</td>
                    <td>{baris.cabang}</td><td>{baris.nama_instansi || "—"}</td><td>{baris.produk}</td>
                    <td>{baris.serial_number || "—"}</td><td className={styles.num}>{baris.qty}</td>
                    <td className={styles.num}>{uang.format(baris.subtotal)}</td><td>{baris.status}</td>
                  </tr>
                ))}</tbody>
              </table>
            </div>
          ) : <EmptyState icon="chart-line">Belum ada baris laporan untuk filter ini.</EmptyState>}
          <div className={styles.pagination}>
            <span>Halaman {halaman} dari {jumlahHalaman}</span>
            <Button variant="secondary" disabled={halaman <= 1 || laporan.memuat} onClick={() => setHalaman((n) => n - 1)}>Sebelumnya</Button>
            <Button variant="secondary" disabled={halaman >= jumlahHalaman || laporan.memuat} onClick={() => setHalaman((n) => n + 1)}>Berikutnya</Button>
          </div>
        </Card>
      </div>
    </PenjagaSuperadmin>
  );
}
