import { describe, expect, it } from "vitest";

import { hitungTarif, persenKeRasio, rasioKePersen } from "./tarif";

describe("komponen tarif matriks harga", () => {
  it("pratinjau tarif memakai rumus dan pembulatan half-up yang sama dengan server", () => {
    // Contoh normatif master-impact-import.md §7.
    expect(hitungTarif(100000, "0.1", 2500)).toBe(112500);
    expect(hitungTarif(10001, "0.05", 0)).toBe(10501);
    expect(hitungTarif(10005, "0.1", 0)).toBe(11006);
  });

  it("persen yang diketik dan rasio API saling bolak-balik tanpa kehilangan presisi", () => {
    for (const persen of ["0", "5", "10", "2,5", "12,3456", "100"]) {
      const rasio = persenKeRasio(persen);
      expect(rasio).not.toBeNull();
      expect(rasioKePersen(rasio as string)).toBe(persen);
    }
    expect(persenKeRasio("10")).toBe("0.1");
    expect(persenKeRasio("100,5")).toBeNull();
    expect(persenKeRasio("1,23456")).toBeNull();
  });
});
