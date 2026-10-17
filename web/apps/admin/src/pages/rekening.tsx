import { useCallback, useEffect, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  Form,
  Input,
  Modal,
  Notice,
  PageHead,
  Select,
  SkeletonList,
  SwitchRow,
  useToast,
} from "@grahita/ui";

import { PenjagaSuperadmin } from "./shell-pages";
import { GridKartu, KartuMaster, kartuMasterStyles } from "./kartu-master";

/** Rekening adalah satu rekening bank tujuan transfer (PRD §4.6). */
export interface Rekening {
  id: string;
  bank: "BCA" | "BRI";
  nama_pemilik: string;
  nomor: string;
  is_active: boolean;
}

interface FormRekening {
  id?: string;
  bank: "BCA" | "BRI";
  nama_pemilik: string;
  nomor: string;
  is_active: boolean;
}

const REKENING_BARU: FormRekening = {
  bank: "BCA",
  nama_pemilik: "",
  nomor: "",
  is_active: true,
};

function pesanGalat(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Terjadi kesalahan. Coba lagi.";
}

/**
 * RekeningPage — /rekening (Superadmin): pengelolaan rekening bank tujuan
 * transfer. Rekening aktif dipakai pada instruksi pembayaran dan referensi
 * pencocokan rekening tujuan verifikasi. Manager tidak melihat menu maupun
 * halaman ini, dan API tetap menolaknya (PRD §2.1, §4.6).
 *
 * Pemuatannya berada di komponen dalam yang baru dipasang setelah penjaga
 * peran lolos, sehingga Manager tidak pernah memanggil API rekening.
 */
export function RekeningPage() {
  return (
    <PenjagaSuperadmin>
      <RekeningContent />
    </PenjagaSuperadmin>
  );
}

function RekeningContent() {
  const { tampilkan } = useToast();
  const [rows, setRows] = useState<Rekening[] | null>(null);
  const [memuat, setMemuat] = useState(true);
  const [galat, setGalat] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState("");
  const [form, setForm] = useState<FormRekening | null>(null);
  const [galatForm, setGalatForm] = useState<string | null>(null);
  const [errorFields, setErrorFields] = useState<Record<string, string>>({});

  const muat = useCallback(async () => {
    setMemuat(true);
    try {
      setRows(await api.get<Rekening[]>("/admin/bank-accounts"));
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, []);

  useEffect(() => {
    void muat();
  }, [muat]);

  async function simpan() {
    if (!form) return;
    setSibuk("form");
    setGalatForm(null);
    setErrorFields({});
    const muatan = {
      bank: form.bank,
      nama_pemilik: form.nama_pemilik,
      nomor: form.nomor,
      is_active: form.is_active,
    };
    try {
      if (form.id) {
        await api.put(`/admin/bank-accounts/${form.id}`, muatan);
        tampilkan(`Rekening ${form.bank} ${form.nomor} disimpan`);
      } else {
        await api.post("/admin/bank-accounts", muatan);
        tampilkan(`Rekening ${form.bank} ditambahkan`);
      }
      setForm(null);
      await muat();
    } catch (err) {
      setGalatForm(pesanGalat(err));
      if (err instanceof ApiClientError) setErrorFields(err.mapFields);
    } finally {
      setSibuk("");
    }
  }

  function bukaDetail(row: Rekening) {
    setGalatForm(null);
    setErrorFields({});
    setForm({ id: row.id, bank: row.bank, nama_pemilik: row.nama_pemilik, nomor: row.nomor, is_active: row.is_active });
  }

  return (
    <>
      <PageHead
        title="Rekening bank"
        sub="Rekening aktif dipakai pada instruksi pembayaran dan pencocokan verifikasi"
        actions={
          <Button
            variant="primary"
            icon="plus"
            onClick={() => {
              setGalatForm(null);
              setErrorFields({});
              setForm({ ...REKENING_BARU });
            }}
          >
            Tambah rekening
          </Button>
        }
      />
      {galat ? (
        <div style={{ marginBottom: 12 }}>
          <Notice tone="danger">{galat}</Notice>
        </div>
      ) : null}
      {memuat ? (
        <Card>
          <SkeletonList rows={3} />
        </Card>
      ) : !rows || rows.length === 0 ? (
        <Card>
          <EmptyState icon="bank">Belum ada rekening terdaftar.</EmptyState>
        </Card>
      ) : (
        <GridKartu label="Rekening bank">
          {rows.map((row) => (
            <KartuMaster
              key={row.id}
              judul={row.bank}
              redup={!row.is_active}
              onBuka={() => bukaDetail(row)}
              status={<Badge tone={row.is_active ? "success" : "neutral"}>{row.is_active ? "Aktif" : "Nonaktif"}</Badge>}
            >
              <span className={kartuMasterStyles.angka}>{row.nomor}</span>
              <span className="muted small">{`a.n. ${row.nama_pemilik}`}</span>
            </KartuMaster>
          ))}
        </GridKartu>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>
        Klik kartu untuk melihat dan mengubah detail rekening. Rekening nonaktif tidak ditampilkan pada
        instruksi pembayaran Customer Portal.
      </p>

      <Modal
        open={form !== null}
        title={form?.id ? "Detail rekening" : "Tambah rekening"}
        onClose={() => setForm(null)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setForm(null)}>
              Batal
            </Button>
            <Button
              variant="primary"
              disabled={sibuk === "form" || !form?.nama_pemilik || !form?.nomor}
              onClick={() => void simpan()}
            >
              Simpan
            </Button>
          </>
        }
      >
        {form ? (
          <Form>
            {galatForm ? <Notice tone="danger">{galatForm}</Notice> : null}
            <Field label="Bank" htmlFor="rek-bank" error={errorFields.bank}>
              <Select
                id="rek-bank"
                value={form.bank}
                onChange={(e) => setForm({ ...form, bank: e.target.value as FormRekening["bank"] })}
              >
                <option value="BCA">BCA</option>
                <option value="BRI">BRI</option>
              </Select>
            </Field>
            <Field label="Nama di rekening" htmlFor="rek-nama" error={errorFields.nama_pemilik}>
              <Input
                id="rek-nama"
                value={form.nama_pemilik}
                onChange={(e) => setForm({ ...form, nama_pemilik: e.target.value })}
              />
            </Field>
            <Field
              label="Nomor rekening"
              htmlFor="rek-nomor"
              hint="Hanya angka, 6–34 digit."
              error={errorFields.nomor}
            >
              <Input
                id="rek-nomor"
                inputMode="numeric"
                value={form.nomor}
                onChange={(e) => setForm({ ...form, nomor: e.target.value })}
              />
            </Field>
            <SwitchRow
              checked={form.is_active}
              onChange={(v) => setForm({ ...form, is_active: v })}
              label="Aktif"
              hint="Hanya rekening aktif yang muncul di instruksi pembayaran."
            />
          </Form>
        ) : null}
      </Modal>
    </>
  );
}

