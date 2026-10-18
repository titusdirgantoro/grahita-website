import { useState } from "react";

import {
  Badge,
  Button,
  Card,
  CardHead,
  Chip,
  CustomerShell,
  EmptyState,
  Field,
  Form,
  Input,
  Modal,
  Notice,
  PageHead,
  Segmented,
  Skeleton,
  SkeletonList,
  Switch,
  Table,
  Textarea,
  initials,
} from "@grahita/ui";

import { NAV_CUSTOMER } from "../nav";

const CONTOH_PESANAN = [
  { nomor: "2026-10-01", tanggal: "1 Okt 2026", item: 3, total: "Rp 1.240.000", status: "Selesai" },
  { nomor: "2026-10-02", tanggal: "7 Okt 2026", item: 1, total: "Rp 350.000", status: "Menunggu" },
];

/**
 * GayaPage — halaman contoh sistem desain customer.
 *
 * Menampilkan shell + seluruh komponen kit memakai akun contoh, tanpa memanggil
 * API. Dipakai QA/desainer untuk membandingkan tampilan dengan prototype
 * (customer_v2.html) pada layar desktop maupun mobile.
 */
export function GayaPage() {
  const [segmen, setSegmen] = useState<"desktop" | "mobile">("desktop");
  const [modal, setModal] = useState(false);
  const [chipAktif, setChipAktif] = useState(true);
  const [switchAktif, setSwitchAktif] = useState(true);

  return (
    <CustomerShell
      nav={NAV_CUSTOMER}
      active="/katalog"
      account={{ nama: "Grahita Bandung", email: "bandung@grahitaindonesia.com" }}
    >
      <PageHead title="Sistem desain" sub="Contoh komponen portal cabang (F1-03)" />

      <div style={{ display: "grid", gap: 16 }}>
        <Card>
          <CardHead
            title="Segmented & chip"
            tools={
              <Segmented
                label="Contoh segmen"
                options={[
                  { value: "desktop", label: "Desktop" },
                  { value: "mobile", label: "Mobile" },
                ]}
                value={segmen}
                onChange={setSegmen}
              />
            }
          />
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
            <Chip pressed={chipAktif} onToggle={setChipAktif}>
              Menunggu verifikasi
            </Chip>
            <Chip pressed={false} onToggle={() => undefined} onRemove={() => undefined}>
              Email gagal
            </Chip>
            <Switch checked={switchAktif} onChange={setSwitchAktif} label="Contoh switch" />
          </div>
        </Card>

        <Card>
          <CardHead title="Tombol" />
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            <Button variant="primary" icon="plus">
              Tombol utama
            </Button>
            <Button variant="secondary" icon="upload-simple">
              Sekunder
            </Button>
            <Button variant="ghost">Ghost</Button>
            <Button variant="danger-ghost" icon="trash">
              Hapus
            </Button>
            <Button variant="primary" disabled>
              Nonaktif
            </Button>
            <Button iconOnly icon="bell" aria-label="Notifikasi" />
            <Button variant="secondary" onClick={() => setModal(true)}>
              Buka modal
            </Button>
          </div>
        </Card>

        <Card>
          <CardHead title="Form" />
          <Form>
            <Field label="Nama cabang" htmlFor="g-nama">
              <Input id="g-nama" placeholder="Grahita …" />
            </Field>

            <Field label="Catatan" htmlFor="g-catatan" hint="Opsional.">
              <Textarea id="g-catatan" rows={3} />
            </Field>
            <Field label="Harga" htmlFor="g-harga" error="Harga wajib diisi.">
              <Input id="g-harga" invalid placeholder="0" />
            </Field>
          </Form>
        </Card>

        <Card>
          <CardHead title="Status & badge" />
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            <Badge tone="draft">Draft</Badge>
            <Badge tone="warning">Menunggu verifikasi</Badge>
            <Badge tone="danger">Perlu review</Badge>
            <Badge tone="success">Selesai</Badge>
            <Badge tone="info">Sertifikat</Badge>
            <Badge tone="cnc">C&amp;C</Badge>
            <Badge tone="rev">Revisi 1</Badge>
          </div>
        </Card>

        <Card>
          <CardHead title="Tabel" />
          <Table mobileCards caption="Contoh tabel pesanan">
            <thead>
              <tr>
                <th scope="col">Nomor</th>
                <th scope="col">Tanggal</th>
                <th scope="col" className="num">
                  Item
                </th>
                <th scope="col" className="num">
                  Total
                </th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {CONTOH_PESANAN.map((p) => (
                <tr key={p.nomor}>
                  <td>{p.nomor}</td>
                  <td>{p.tanggal}</td>
                  <td className="num">{p.item}</td>
                  <td className="num">{p.total}</td>
                  <td>
                    <Badge tone={p.status === "Selesai" ? "success" : "warning"}>{p.status}</Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>

        <Card>
          <CardHead title="Notifikasi" />
          <div style={{ display: "grid", gap: 8 }}>
            <Notice tone="info">Informasi umum untuk cabang.</Notice>
            <Notice tone="warning">Perhatian: saldo deposit belum menutup seluruh pesanan.</Notice>
            <Notice tone="success">Pembayaran terverifikasi, invoice siap diunduh.</Notice>
            <Notice tone="danger">Bukti transfer tidak terbaca, mohon unggah ulang.</Notice>
          </div>
        </Card>

        <Card>
          <CardHead title="State kosong & skeleton" />
          <EmptyState icon="receipt">Belum ada pesanan untuk ditampilkan.</EmptyState>
          <SkeletonList rows={2} />
        </Card>

        <Card>
          <CardHead title="Avatar" />
          <div style={{ display: "flex", gap: 12, alignItems: "center" }}>
            <span className="muted">{initials("Grahita Bandung")}</span>
            <Skeleton variant="circle" />
          </div>
        </Card>
      </div>

      <Modal
        open={modal}
        onClose={() => setModal(false)}
        title="Contoh modal"
        footer={
          <Button variant="primary" onClick={() => setModal(false)}>
            Selesai
          </Button>
        }
      >
        <p className="muted">
          Modal bersama: fokus masuk ke kontrol pertama, Tab terperangkap, Escape menutup.
        </p>
      </Modal>
    </CustomerShell>
  );
}
