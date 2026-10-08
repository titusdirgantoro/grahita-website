import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { Accordion } from "../components/Accordion";
import { Avatar, initials } from "../components/Avatar";
import { Badge, Chip, PillNote } from "../components/Badge";
import { Button, ButtonLink } from "../components/Button";
import { Card, CardHead } from "../components/Card";
import { EmptyState } from "../components/EmptyState";
import { Field, Form, Input, Select, Textarea } from "../components/Field";
import { Modal } from "../components/Modal";
import { Notice } from "../components/Notice";
import { PageHead } from "../components/PageHead";
import { Segmented } from "../components/Segmented";
import { Skeleton, SkeletonList } from "../components/Skeleton";
import { Switch, SwitchRow } from "../components/Switch";
import { Table } from "../components/Table";
import { ToastProvider, useToast } from "../components/Toast";

describe("Button", () => {
  it("merender varian primary sebagai tombol berkelas .btn", () => {
    render(<Button variant="primary">Simpan</Button>);
    const btn = screen.getByRole("button", { name: "Simpan" });
    expect(btn.className).toContain("btn");
    expect(btn.className).toContain("primary");
    expect(btn.getAttribute("type")).toBe("button");
  });

  it("tombol hanya-ikon memakai kelas .icon-btn dan wajib punya aria-label", () => {
    render(<Button iconOnly icon="x" aria-label="Tutup" />);
    const btn = screen.getByRole("button", { name: "Tutup" });
    expect(btn.className).toContain("iconBtn");
  });

  it("meneruskan disabled dan tidak memanggil onClick saat nonaktif", () => {
    const onClick = vi.fn();
    render(
      <Button disabled onClick={onClick}>
        Kirim
      </Button>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Kirim" }));
    expect(onClick).not.toHaveBeenCalled();
  });

  it("ButtonLink merender tautan dengan href", () => {
    render(
      <ButtonLink href="/katalog" variant="secondary">
        Katalog
      </ButtonLink>,
    );
    const a = screen.getByRole("link", { name: "Katalog" });
    expect(a.getAttribute("href")).toBe("/katalog");
    expect(a.className).toContain("secondary");
  });
});

describe("Field, Input, Textarea, Select", () => {
  it("menghubungkan label dengan kontrol dan menampilkan error", () => {
    render(
      <Field label="Email cabang" htmlFor="em" error="Email wajib diisi.">
        <Input id="em" type="email" />
      </Field>,
    );
    const input = screen.getByLabelText("Email cabang");
    expect(input.getAttribute("type")).toBe("email");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.className).toContain("invalid");
    expect(screen.getByRole("alert").textContent).toContain("Email wajib diisi.");
  });

  it("menampilkan hint hanya bila tidak ada error", () => {
    render(
      <Field label="Password" htmlFor="pw" hint="Minimal 8 karakter.">
        <Input id="pw" type="password" />
      </Field>,
    );
    expect(screen.getByText("Minimal 8 karakter.")).toBeDefined();
  });

  it("Textarea dan Select memakai kelas input yang sama", () => {
    render(
      <Form>
        <Field label="Catatan" htmlFor="cat">
          <Textarea id="cat" />
        </Field>
        <Field label="Satuan" htmlFor="sat">
          <Select id="sat" defaultValue="pcs">
            <option value="pcs">pcs</option>
          </Select>
        </Field>
      </Form>,
    );
    expect(screen.getByLabelText("Catatan").className).toContain("input");
    expect(screen.getByLabelText("Satuan").className).toContain("input");
  });
});

describe("Badge & Chip", () => {
  it("Badge memakai kelas tone yang diminta", () => {
    render(<Badge tone="cnc">C&amp;C</Badge>);
    const badge = screen.getByText("C&C");
    expect(badge.className).toContain("badge");
    expect(badge.className).toContain("cnc");
  });

  it("Chip memakai aria-pressed dan memanggil onToggle", () => {
    const onToggle = vi.fn();
    render(
      <Chip pressed={false} onToggle={onToggle}>
        2 menunggu
      </Chip>,
    );
    const chip = screen.getByRole("button", { name: /2 menunggu/ });
    expect(chip.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(chip);
    expect(onToggle).toHaveBeenCalledWith(true);
  });

  it("Chip menyediakan tombol silang yang memanggil onRemove", () => {
    const onRemove = vi.fn();
    render(
      <Chip onRemove={onRemove} removeLabel="Sembunyikan chip">
        perlu review
      </Chip>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Sembunyikan chip" }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  it("PillNote merender teks", () => {
    render(<PillNote>dibuat otomatis</PillNote>);
    expect(screen.getByText("dibuat otomatis")).toBeDefined();
  });
});

describe("Card & PageHead", () => {
  it("CardHead merender judul sebagai heading", () => {
    render(
      <Card>
        <CardHead title="Daftar pesanan" tools={<span>alat</span>} />
      </Card>,
    );
    expect(screen.getByRole("heading", { name: "Daftar pesanan" })).toBeDefined();
    expect(screen.getByText("alat")).toBeDefined();
  });

  it("PageHead merender judul, subjudul, dan aksi", () => {
    render(<PageHead title="Katalog" sub="Produk aktif" actions={<Button>Buat</Button>} />);
    expect(screen.getByRole("heading", { name: "Katalog" })).toBeDefined();
    expect(screen.getByText("Produk aktif")).toBeDefined();
    expect(screen.getByRole("button", { name: "Buat" })).toBeDefined();
  });
});

describe("Table", () => {
  it("merender tabel dengan caption untuk pembaca layar", () => {
    render(
      <Table mobileCards caption="Daftar pesanan cabang">
        <thead>
          <tr>
            <th scope="col">Nomor</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>2026-10-01</td>
          </tr>
        </tbody>
      </Table>,
    );
    expect(screen.getByText("Daftar pesanan cabang")).toBeDefined();
    expect(screen.getByRole("table")).toBeDefined();
    expect(screen.getByText("2026-10-01")).toBeDefined();
  });
});

describe("EmptyState, Skeleton, Notice, Accordion", () => {
  it("EmptyState menampilkan pesan dan aksi", () => {
    render(
      <EmptyState icon="receipt" action={<Button>Buat pesanan</Button>}>
        Belum ada pesanan.
      </EmptyState>,
    );
    expect(screen.getByText("Belum ada pesanan.")).toBeDefined();
    expect(screen.getByRole("button", { name: "Buat pesanan" })).toBeDefined();
  });

  it("Skeleton disembunyikan dari pembaca layar dan SkeletonList aria-busy", () => {
    const { container } = render(
      <>
        <Skeleton variant="block" />
        <SkeletonList rows={2} />
      </>,
    );
    expect(container.querySelectorAll("[aria-hidden='true']").length).toBeGreaterThan(0);
    expect(container.querySelector("[aria-busy='true']")).not.toBeNull();
  });

  it("Notice danger memakai role alert, tone lain memakai status", () => {
    render(
      <>
        <Notice tone="danger">Gagal</Notice>
        <Notice tone="info">Info</Notice>
      </>,
    );
    expect(screen.getByRole("alert").textContent).toContain("Gagal");
    expect(screen.getByRole("status").textContent).toContain("Info");
  });

  it("Accordion merender ringkasan yang bisa dibuka", () => {
    render(
      <Accordion summary="Cara memesan" open>
        <li>Langkah satu</li>
      </Accordion>,
    );
    expect(screen.getByText("Cara memesan")).toBeDefined();
    expect(screen.getByText("Langkah satu")).toBeDefined();
  });
});

describe("Segmented & Switch", () => {
  it("Segmented menandai pilihan aktif dan melaporkan perubahan", () => {
    const onChange = vi.fn();
    render(
      <Segmented
        label="Periode"
        options={[
          { value: "hari", label: "Hari ini" },
          { value: "bulan", label: "Bulan ini" },
        ]}
        value="bulan"
        onChange={onChange}
      />,
    );
    expect(screen.getByRole("button", { name: "Bulan ini" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("group", { name: "Periode" })).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Hari ini" }));
    expect(onChange).toHaveBeenCalledWith("hari");
  });

  it("Switch memakai peran switch dan membalik nilai", () => {
    const onChange = vi.fn();
    render(<Switch checked={false} onChange={onChange} label="Aktifkan AI" />);
    const sw = screen.getByRole("switch", { name: "Aktifkan AI" });
    expect(sw.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("SwitchRow menampilkan label dan hint", () => {
    render(
      <SwitchRow checked onChange={() => undefined} label="Manager boleh revisi" hint="Kunci X" />,
    );
    expect(screen.getByText("Manager boleh revisi")).toBeDefined();
    expect(screen.getByText("Kunci X")).toBeDefined();
    expect(screen.getByRole("switch")).toBeDefined();
  });
});

describe("Avatar", () => {
  it("initials mengabaikan awalan Grahita dan mengambil dua huruf awal kata", () => {
    expect(initials("Grahita Bandung")).toBe("B");
    expect(initials("Dra. Sri Wahyuni")).toBe("DS");
  });

  it("Avatar menyembunyikan inisial dari pembaca layar", () => {
    const { container } = render(<Avatar nama="Grahita Bandung" />);
    const el = container.querySelector("[aria-hidden='true']");
    expect(el?.textContent).toBe("B");
  });
});

describe("Modal", () => {
  it("tidak merender apa pun saat tertutup", () => {
    render(
      <Modal open={false} onClose={() => undefined} title="Kirim">
        isi
      </Modal>,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("merender dialog dengan aria-modal dan menutup lewat tombol Tutup", () => {
    const onClose = vi.fn();
    render(
      <Modal
        open
        onClose={onClose}
        title="Kirim pesanan"
        footer={<Button variant="primary">Kirim</Button>}
      >
        <p>Unggah bukti transfer.</p>
      </Modal>,
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(screen.getByText("Kirim pesanan")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Tutup" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("menutup saat tombol Escape ditekan", () => {
    const onClose = vi.fn();
    render(
      <Modal open onClose={onClose} title="Perhatian">
        <p>isi</p>
      </Modal>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("memindahkan fokus ke kontrol pertama saat dibuka", () => {
    render(
      <Modal open onClose={() => undefined} title="Fokus">
        <Button variant="primary">Aksi pertama</Button>
      </Modal>,
    );
    expect(document.activeElement?.textContent).toBe("Aksi pertama");
  });
});

describe("Toast", () => {
  function Pemicu() {
    const { tampilkan } = useToast();
    return <Button onClick={() => tampilkan("Tersimpan")}>Simpan</Button>;
  }

  it("menampilkan pesan dalam area aria-live", async () => {
    render(
      <ToastProvider>
        <Pemicu />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));
    expect(await screen.findByText("Tersimpan")).toBeDefined();
    expect(screen.getByRole("status").textContent).toContain("Tersimpan");
  });
});
