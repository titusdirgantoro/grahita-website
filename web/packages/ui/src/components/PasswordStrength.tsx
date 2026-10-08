import { kekuatanKataSandi } from "../password";
import { cn } from "../cn";
import styles from "./PasswordStrength.module.css";

export interface PasswordStrengthProps {
  /** Kata sandi yang sedang diketik. Kosong berarti indikator tidak tampil. */
  password: string;
  /** Label aksesibilitas meter (bawaan "Kekuatan kata sandi"). */
  label?: string;
  className?: string;
}

/**
 * PasswordStrength adalah indikator kekuatan kata sandi (PRD CP-REG-05).
 *
 * Ini HANYA umpan balik visual: aturan yang mengikat tetap di backend
 * (`auth.CheckPasswordStrength` — minimal 8 karakter, memuat huruf dan angka).
 * Indikator baru muncul setelah pengguna mulai mengetik supaya tidak menambah
 * kebisingan di samping petunjuk field.
 *
 * Meter diberi `role="meter"` + `aria-valuenow` dan labelnya diumumkan lewat
 * `aria-live="polite"` sehingga pembaca layar mengikuti perubahan kekuatan.
 */
export function PasswordStrength({ password, label = "Kekuatan kata sandi", className }: PasswordStrengthProps) {
  if (password === "") return null;
  const { skor, label: teks } = kekuatanKataSandi(password);

  return (
    <div className={cn(styles.kekuatan, className)} data-skor={skor}>
      <div
        className={styles.meter}
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={4}
        aria-valuenow={skor}
        aria-valuetext={teks}
      >
        {[1, 2, 3, 4].map((n) => (
          <span key={n} className={n <= skor ? cn(styles.batang, styles.aktif) : styles.batang} />
        ))}
      </div>
      <span className={styles.label} aria-live="polite">
        {label}: <b>{teks}</b>
      </span>
    </div>
  );
}
