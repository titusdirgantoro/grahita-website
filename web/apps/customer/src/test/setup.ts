import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

/**
 * Setup pengujian komponen.
 *
 * Auto-cleanup bawaan Testing Library hanya aktif bila `globals` menyala;
 * karena proyek ini memakai import eksplisit dari vitest, pembersihan DOM dan
 * global tiruan antar-tes dilakukan di sini supaya markup tes sebelumnya tidak
 * bocor ke tes berikutnya.
 */
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
