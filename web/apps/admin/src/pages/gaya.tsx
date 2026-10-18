import { useState } from "react";

import {
  AdminShell,
  Badge,
  Button,
  Card,
  CardHead,
  Chip,
  EmptyState,
  Field,
  Form,
  Input,
  Modal,
  Notice,
  PageHead,
  Segmented,
  Select,
  Skeleton,
  SkeletonList,
  Switch,
  Table,
  Textarea,
  type Role,
} from "@grahita/ui";

import { NAV_ADMIN } from "../nav";

const CONTOH = [
  { nomor: "2026-10-01", cabang: "Grahita Bandung", total: "Rp 1.240.000", status: "Selesai" },
  { nomor: "2026-10-02", cabang: "Grahita Cimahi", total: "Rp 350.000", status: "Menunggu" },
];

/**
 * GayaPage — halaman contoh sistem desain admin.
 *
 * Memuat shell + seluruh komponen kit dengan akun contoh. Sakelar peran
 * memperlihatkan guard menu: Manager kehilangan Rekening bank, Reporting, dan
 * Pengaturan. Dipakai QA untuk membandingkan tampilan dengan admin_v2.html.
 */
export function GayaPage() {
  const [peran, setPeran] = useState<Role>("superadmin");
  const [modal, setModal] = useState(false);
  const [switchAktif, setSwitchAktif] = useState(true);

  return (
    <AdminShell
      groups={NAV_ADMIN}
      active="/cabang"
      account={{ nama: peran === "superadmin" ? "Rina Kartika" : "Budi Santoso", role: peran }}
      topLeft={
        <Select defaultValue="all" aria-label="Filter cabang">
          <option value="all">Semua cabang</option>
          <option value="1">Grahita Bandung</option>
        </Select>
      }
      topCenter={
        <Segmented
          label="Contoh peran"
          options={[
            { value: "superadmin", label: "Superadmin" },
            { value: "manager", label: "Manager" },
          ]}
          value={peran}
          onChange={setPeran}
        />
      }
      topRight={
        <Button iconOnly icon="bell" aria-label="Perlu tindakan" />
      }
    >
      <PageHead title="Sistem desain" sub="Contoh komponen admin portal (F1-03)" />

      <div style={{ display: "grid", gap: 16 }}>
        <Card>
          <CardHead title="Guard menu peran" />
          <Notice tone="info">
            Peran aktif: {peran === "superadmin" ? "Superadmin" : "Manager"}. Item Superadmin
            (Rekening bank, Reporting, Pengaturan) otomatis hilang dari sidebar dan halamannya
            menolak akses.
          </Notice>
        </Card>

        <Card>
          <CardHead title="Tombol" />
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
            <Button variant="primary" icon="plus">
              Utama
            </Button>
            <Button variant="secondary" icon="download-simple">
              Sekunder
            </Button>
            <Button variant="ghost">Ghost</Button>
            <Button variant="danger-ghost" icon="trash">
              Hapus
            </Button>
            <Button iconOnly icon="funnel" aria-label="Filter" />
            <Button variant="secondary" onClick={() => setModal(true)}>
              Buka modal
            </Button>
          </div>
        </Card>

        <Card>
          <CardHead title="Chip, badge, switch" />
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
            <Chip pressed onToggle={() => undefined}>
              2 menunggu verifikasi
            </Chip>
            <Chip pressed={false} onToggle={() => undefined} onRemove={() => undefined}>
              1 perlu review
            </Chip>
            <Switch checked={switchAktif} onChange={setSwitchAktif} label="Contoh switch" />
          </div>
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginTop: 12 }}>
            <Badge tone="draft">Draft</Badge>
            <Badge tone="warning">Menunggu verifikasi</Badge>
            <Badge tone="danger">Perlu review</Badge>
            <Badge tone="success">Selesai</Badge>
            <Badge tone="cnc">C&amp;C</Badge>
            <Badge tone="rev">Revisi 1</Badge>
          </div>
        </Card>

        <Card>
          <CardHead title="Form" />
          <Form>
            <Field label="Nama produk" htmlFor="ag-nama">
              <Input id="ag-nama" placeholder="Bio Magnetik" />
            </Field>
            <Field label="Jenis" htmlFor="ag-jenis">
              <Select id="ag-jenis" defaultValue="reguler">
                <option value="reguler">Reguler</option>
                <option value="sertifikat">Sertifikat</option>
              </Select>
            </Field>
            <Field label="Catatan internal" htmlFor="ag-catatan">
              <Textarea id="ag-catatan" rows={3} />
            </Field>
            <Field label="Harga default" htmlFor="ag-harga" error="Harga wajib diisi.">
              <Input id="ag-harga" invalid placeholder="0" />
            </Field>
          </Form>
        </Card>

        <Card>
          <CardHead title="Tabel" />
          <Table mobileCards caption="Contoh tabel pesanan admin">
            <thead>
              <tr>
                <th scope="col">Nomor</th>
                <th scope="col">Cabang</th>
                <th scope="col" className="num">
                  Total
                </th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {CONTOH.map((p) => (
                <tr key={p.nomor}>
                  <td>{p.nomor}</td>
                  <td>{p.cabang}</td>
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
          <CardHead title="State kosong, skeleton, notice" />
          <EmptyState icon="storefront">Belum ada data untuk ditampilkan.</EmptyState>
          <SkeletonList rows={2} />
          <div style={{ display: "grid", gap: 8, marginTop: 12 }}>
            <Notice tone="warning">Saldo deposit cabang belum menutup seluruh pesanan.</Notice>
            <Notice tone="danger">Email invoice gagal terkirim.</Notice>
          </div>
          <Skeleton variant="block" />
        </Card>
      </div>

      <Modal
        open={modal}
        onClose={() => setModal(false)}
        wide
        title="Contoh modal lebar"
        footer={
          <>
            <Button variant="secondary" onClick={() => setModal(false)}>
              Batal
            </Button>
            <Button variant="primary" onClick={() => setModal(false)}>
              Simpan
            </Button>
          </>
        }
      >
        <p className="muted">Modal `wide` (880px) dipakai untuk verifikasi bukti transfer dan revisi.</p>
      </Modal>
    </AdminShell>
  );
}
