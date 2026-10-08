import { useState, type ReactNode } from "react";

import {
  Badge,
  Button,
  Card,
  CardHead,
  Chip,
  EmptyState,
  Field,
  Form,
  Input,
  Notice,
  PageHead,
  Segmented,
  Select,
  Skeleton,
  SkeletonList,
  SwitchRow,
  Table,
} from "@grahita/ui";

import { useSesi } from "../session";
import { useApi } from "../useApi";
import styles from "./admin-pages.module.css";

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

const PERIODE = [
  { value: "hari", label: "Hari ini" },
  { value: "minggu", label: "Minggu ini" },
  { value: "bulan", label: "Bulan ini" },
  { value: "tahun", label: "Tahun ini" },
] as const;

interface Cabang {
  id: string;
  nama: string;
  nama_kepala: string;
  email: string;
  telepon?: string;
  is_cnc: boolean;
  is_special?: boolean;
  is_active: boolean;
}

interface Produk {
  id: string;
  nama: string;
  jenis: string;
  satuan: string;
  harga_default: number;
  is_active: boolean;
}

/** MiniBars adalah grafik SVG buatan sendiri (bukan library), sesuai §7.2. */
function MiniBars({ nilai, label }: { nilai: number[]; label: string }) {
  const lebarBatang = 10;
  const celah = 6;
  const tinggi = 48;
  const maks = Math.max(...nilai, 1);
  return (
    <div className={styles.chartWrap}>
      <svg
        width={nilai.length * (lebarBatang + celah) - celah}
        height={tinggi}
        role="img"
        aria-label={label}
      >
        {nilai.map((v, i) => {
          const h = v > 0 ? Math.max(4, (tinggi * v) / maks) : 4;
          return (
            <rect
              key={i}
              x={i * (lebarBatang + celah)}
              y={tinggi - h}
              width={lebarBatang}
              height={h}
              rx={2}
              style={{ fill: i === nilai.length - 1 ? "var(--brand-500)" : "var(--brand-200)" }}
            />
          );
        })}
      </svg>
    </div>
  );
}

/** PenjagaSuperadmin menolak halaman Superadmin untuk Manager (guard rute). */
export function PenjagaSuperadmin({ children }: { children: ReactNode }) {
  const { peran } = useSesi();
  if (peran !== "superadmin") {
    return (
      <>
        <PageHead title="Tidak berwenang" sub="Halaman ini khusus Superadmin" />
        <Card>
          <Notice tone="warning">
            Akun Anda berperan Manager. Menu Superadmin disembunyikan dan halamannya menolak akses.
            Hubungi Superadmin bila membutuhkan akses ini.
          </Notice>
          <div className={styles.row} style={{ marginTop: 16 }}>
            <Button variant="secondary" icon="arrow-left">
              Kembali ke dashboard
            </Button>
          </div>
        </Card>
      </>
    );
  }
  return <>{children}</>;
}

/** pDashboard — /: filter cabang + periode, chip perlu tindakan, grafik SVG. */
export function DashboardPage() {
  const [periode, setPeriode] = useState<(typeof PERIODE)[number]["value"]>("bulan");
  const [filter, setFilter] = useState("all");
  const [chip, setChip] = useState<Record<string, boolean>>({
    menunggu: true,
    review: true,
    lewat: true,
    email: true,
  });
  const { peran } = useSesi();
  const { data: cabang } = useApi<Cabang[]>("/admin/branches");

  return (
    <>
      <PageHead title="Dashboard" sub="Ringkasan seluruh cabang" />

      <div className={styles.stack}>
        <Card>
          <CardHead
            title="Filter"
            tools={
              <Segmented label="Periode" options={PERIODE} value={periode} onChange={setPeriode} />
            }
          />
          <div className={styles.row}>
            <Field label="Cabang" htmlFor="dash-cabang">
              <Select id="dash-cabang" value={filter} onChange={(e) => setFilter(e.target.value)}>
                <option value="all">Semua cabang</option>
                {(cabang ?? []).map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.nama}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
        </Card>

        <Card>
          <CardHead title="Perlu tindakan" />
          <div className={styles.chips}>
            {(
              [
                ["menunggu", "0 menunggu verifikasi"],
                ["review", "0 perlu review"],
                ["lewat", "0 C&C lewat jatuh tempo"],
                ["email", "0 email gagal"],
              ] as const
            ).map(([kunci, teks]) => (
              <Chip
                key={kunci}
                pressed={chip[kunci]}
                onToggle={(v) => setChip((c) => ({ ...c, [kunci]: v }))}
                onRemove={() => setChip((c) => ({ ...c, [kunci]: false }))}
                removeLabel={`Sembunyikan: ${teks}`}
              >
                {teks}
              </Chip>
            ))}
          </div>
        </Card>

        <div className={styles.kpis}>
          <Card>
            <p className={styles.lbl}>Nilai pesanan {periode} ini</p>
            <p className={styles.val}>{peran === "superadmin" ? rupiah.format(0) : "0 pesanan"}</p>
            <span className="caption">
              {peran === "superadmin" ? "Superadmin melihat rupiah" : "Manager melihat jumlah"}
            </span>
          </Card>
          <Card>
            <p className={styles.lbl}>Pesanan selesai</p>
            <p className={styles.val}>0</p>
            <span className="caption">Belum ada pesanan diverifikasi</span>
          </Card>
          <Card>
            <p className={styles.lbl}>Cabang aktif</p>
            <p className={styles.val}>{cabang ? cabang.filter((c) => c.is_active).length : "…"}</p>
            <span className="caption">Dari master cabang</span>
          </Card>
        </div>

        <Card>
          <CardHead title="Tren 12 bulan" />
          <MiniBars nilai={Array.from({ length: 12 }, () => 0)} label="Tren pesanan 12 bulan" />
          <p className="muted small" style={{ marginTop: 8 }}>
            Grafik SVG buatan sendiri; data menyusul pada kartu Reporting/Dashboard.
          </p>
        </Card>

        <Card>
          <CardHead title="Ringkasan status pembayaran" />
          <div className={styles.row}>
            <Badge tone="success">0 terverifikasi</Badge>
            <Badge tone="warning">0 menunggu</Badge>
            <Badge tone="danger">0 perlu review</Badge>
          </div>
        </Card>
      </div>
    </>
  );
}

/** pPesanan (admin) — /pesanan: filter lanjutan + tabel (data menyusul). */
export function PesananAdminPage() {
  return (
    <>
      <PageHead title="Riwayat pemesanan" sub="Seluruh cabang" />
      <Card>
        <CardHead
          title="Daftar pesanan"
          tools={
            <div className={styles.row}>
              <Field label="Cari" htmlFor="cari-pesanan">
                <Input id="cari-pesanan" placeholder="Nomor, cabang, serial…" />
              </Field>
            </div>
          }
        />
        <div className={styles.chips} style={{ marginBottom: 12 }}>
          <Chip pressed onToggle={() => undefined}>
            C&amp;C
          </Chip>
          <Chip pressed={false} onToggle={() => undefined}>
            Lewat jatuh tempo
          </Chip>
          <Chip pressed={false} onToggle={() => undefined}>
            Email gagal
          </Chip>
        </div>
        <Table mobileCards caption="Riwayat pemesanan seluruh cabang">
          <thead>
            <tr>
              <th scope="col">Nomor</th>
              <th scope="col">Cabang</th>
              <th scope="col">Tanggal</th>
              <th scope="col" className="num">
                Total
              </th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td colSpan={5}>
                <EmptyState icon="receipt">Belum ada pesanan untuk filter ini.</EmptyState>
              </td>
            </tr>
          </tbody>
        </Table>
      </Card>
    </>
  );
}

/** pCabang — /cabang: master cabang dari API. */
export function CabangPage() {
  const { data, memuat, galat } = useApi<Cabang[]>("/admin/branches");

  return (
    <>
      <PageHead
        title="Cabang"
        sub="Master data cabang"
        actions={
          <Button variant="primary" icon="plus">
            Tambah cabang
          </Button>
        }
      />
      <Card>
        {memuat ? (
          <SkeletonList rows={3} />
        ) : galat ? (
          <Notice tone="danger">{galat}</Notice>
        ) : !data || data.length === 0 ? (
          <EmptyState icon="storefront">Belum ada cabang terdaftar.</EmptyState>
        ) : (
          <Table mobileCards caption="Master data cabang">
            <thead>
              <tr>
                <th scope="col">Cabang</th>
                <th scope="col">Kepala cabang</th>
                <th scope="col">Email</th>
                <th scope="col">Flag</th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {data.map((c) => (
                <tr key={c.id}>
                  <td>{c.nama}</td>
                  <td>{c.nama_kepala}</td>
                  <td className="muted">{c.email}</td>
                  <td>
                    <div className={styles.row}>
                      {c.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
                      {c.is_special ? <Badge tone="info">Special</Badge> : null}
                    </div>
                  </td>
                  <td>
                    <Badge tone={c.is_active ? "success" : "danger"}>
                      {c.is_active ? "Aktif" : "Nonaktif"}
                    </Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </>
  );
}

/** pKatalog (admin) — /katalog: tab Produk & Matriks harga. */
export function KatalogAdminPage() {
  const [tab, setTab] = useState<"produk" | "matriks">("produk");
  const { data, memuat, galat } = useApi<Produk[]>("/admin/products");

  return (
    <>
      <PageHead
        title="Katalog product"
        sub="Produk dan matriks harga per cabang"
        actions={
          <Button variant="primary" icon="plus">
            Tambah produk
          </Button>
        }
      />
      <Card>
        <CardHead
          title={tab === "produk" ? "Daftar produk" : "Matriks harga"}
          tools={
            <Segmented
              label="Tampilan katalog"
              options={[
                { value: "produk", label: "Produk" },
                { value: "matriks", label: "Matriks harga" },
              ]}
              value={tab}
              onChange={setTab}
            />
          }
        />
        {tab === "produk" ? (
          memuat ? (
            <SkeletonList rows={3} />
          ) : galat ? (
            <Notice tone="danger">{galat}</Notice>
          ) : !data || data.length === 0 ? (
            <EmptyState icon="tag">Belum ada produk pada katalog.</EmptyState>
          ) : (
            <Table mobileCards caption="Daftar produk">
              <thead>
                <tr>
                  <th scope="col">Produk</th>
                  <th scope="col">Jenis</th>
                  <th scope="col">Satuan</th>
                  <th scope="col" className="num">
                    Harga default
                  </th>
                  <th scope="col">Status</th>
                </tr>
              </thead>
              <tbody>
                {data.map((p) => (
                  <tr key={p.id}>
                    <td>{p.nama}</td>
                    <td>{p.jenis}</td>
                    <td>{p.satuan}</td>
                    <td className="num">{rupiah.format(p.harga_default)}</td>
                    <td>
                      <Badge tone={p.is_active ? "success" : "draft"}>
                        {p.is_active ? "Aktif" : "Nonaktif"}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )
        ) : (
          <EmptyState icon="squares-four">
            Matriks harga (Produk × Cabang) dengan edit inline dikerjakan pada kartu katalog admin;
            sel kosong berarti produk tidak dijual ke cabang tersebut.
          </EmptyState>
        )}
      </Card>
    </>
  );
}

/** pRekening — /rekening (Superadmin): rekening bank tujuan transfer. */
export function RekeningPage() {
  return (
    <PenjagaSuperadmin>
      <PageHead
        title="Rekening bank"
        sub="Rekening aktif dipakai pada instruksi pembayaran dan pencocokan verifikasi"
        actions={
          <Button variant="primary" icon="plus">
            Tambah rekening
          </Button>
        }
      />
      <Card>
        <Table mobileCards caption="Rekening bank">
          <thead>
            <tr>
              <th scope="col">Bank</th>
              <th scope="col">Nama</th>
              <th scope="col">Nomor</th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td colSpan={4}>
                <EmptyState icon="bank">Belum ada rekening terdaftar.</EmptyState>
              </td>
            </tr>
          </tbody>
        </Table>
      </Card>
    </PenjagaSuperadmin>
  );
}

/** pReporting — /reporting (Superadmin): laporan per order line + ekspor XLSX. */
export function ReportingPage() {
  return (
    <PenjagaSuperadmin>
      <PageHead
        title="Reporting"
        sub="Satu baris per order line; ekspor Excel diproses asinkron"
        actions={<Button variant="secondary" icon="download-simple">Ekspor Excel</Button>}
      />
      <Card>
        <EmptyState icon="chart-line">
          Belum ada data laporan pada periode ini. Filter tanggal, cabang, dan status akan tersedia
          di sini.
        </EmptyState>
      </Card>
    </PenjagaSuperadmin>
  );
}

/** pPengaturan — /pengaturan (Superadmin): nilai pengaturan sistem. */
export function PengaturanPage() {
  const { data, memuat, galat } = useApi<Record<string, unknown>>("/admin/settings");

  return (
    <PenjagaSuperadmin>
      <PageHead title="Pengaturan" sub="Konstanta sistem (Superadmin)" />
      <div className={styles.stack}>
        <Card>
          <CardHead title="Nilai tersimpan" />
          {memuat ? (
            <div className={styles.stack}>
              <Skeleton variant="title" />
              <Skeleton />
              <Skeleton />
            </div>
          ) : galat ? (
            <Notice tone="danger">{galat}</Notice>
          ) : data && Object.keys(data).length > 0 ? (
            <Table caption="Nilai pengaturan">
              <thead>
                <tr>
                  <th scope="col">Kunci</th>
                  <th scope="col">Nilai</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(data).map(([k, v]) => (
                  <tr key={k}>
                    <td>{k}</td>
                    <td className="tabular muted">{typeof v === "object" ? JSON.stringify(v) : String(v)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          ) : (
            <EmptyState icon="gear-six">Belum ada pengaturan tersimpan.</EmptyState>
          )}
        </Card>

        <Card>
          <CardHead title="Contoh kendali" />
          <SwitchRow
            checked
            onChange={() => undefined}
            label="Manager boleh merevisi invoice"
            hint="Kunci manager_boleh_revisi"
          />
          <SwitchRow
            checked={false}
            onChange={() => undefined}
            label="Verifikasi bukti transfer otomatis (AI)"
            hint="Kunci ai.aktif — fase 2"
          />
          <Form>
            <Field label="Biaya packaging (Rp)" htmlFor="pack">
              <Input id="pack" type="number" defaultValue={3000} />
            </Field>
            <Field label="Tenggat Cash &amp; Carry (hari)" htmlFor="cnc">
              <Input id="cnc" type="number" defaultValue={7} />
            </Field>
            <Button variant="primary">Simpan pengaturan</Button>
          </Form>
        </Card>
      </div>
    </PenjagaSuperadmin>
  );
}

/** pProfil (admin) — /profil: identitas + ganti password. */
export function ProfilAdminPage() {
  const { pengguna, peran } = useSesi();
  return (
    <>
      <PageHead title="Profil" sub="Identitas admin" />
      <div className={styles.stack}>
        <Card>
          <CardHead title="Akun" />
          <Form>
            <Field label="Nama" htmlFor="adm-nama">
              <Input id="adm-nama" readOnly value={pengguna?.nama ?? ""} />
            </Field>
            <Field label="Email" htmlFor="adm-email">
              <Input id="adm-email" readOnly value={pengguna?.email ?? ""} />
            </Field>
            <Field label="Peran" htmlFor="adm-peran">
              <Input id="adm-peran" readOnly value={peran === "superadmin" ? "Superadmin" : "Manager"} />
            </Field>
          </Form>
        </Card>
        <Card>
          <CardHead title="Ganti password" />
          <Form>
            <Field label="Password lama" htmlFor="adm-p1">
              <Input id="adm-p1" type="password" autoComplete="current-password" />
            </Field>
            <Field label="Password baru" htmlFor="adm-p2" hint="Minimal 8 karakter.">
              <Input id="adm-p2" type="password" autoComplete="new-password" />
            </Field>
            <Field label="Konfirmasi password baru" htmlFor="adm-p3">
              <Input id="adm-p3" type="password" autoComplete="new-password" />
            </Field>
            <Button variant="primary">Simpan password</Button>
          </Form>
        </Card>
      </div>
    </>
  );
}
