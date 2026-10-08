import type { ReactNode } from "react";

import { cn } from "../cn";
import styles from "./Switch.module.css";

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  /** Label aksesibilitas; tampil sebagai aria-label (tanpa teks terlihat). */
  label: string;
  disabled?: boolean;
  className?: string;
}

/** Switch adalah `.switch` prototype dengan peran `switch` + `aria-checked`. */
export function Switch({ checked, onChange, label, disabled, className }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className={cn(styles.switch, className)}
      onClick={() => onChange(!checked)}
    />
  );
}

export interface SwitchRowProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: ReactNode;
  hint?: ReactNode;
  disabled?: boolean;
  className?: string;
}

/** SwitchRow meniru `.sw-row`: teks di kiri, switch di kanan. */
export function SwitchRow({
  checked,
  onChange,
  label,
  hint,
  disabled,
  className,
}: SwitchRowProps) {
  const teks = typeof label === "string" ? label : undefined;
  return (
    <div className={cn(styles.row, className)}>
      <div>
        <b>{label}</b>
        {hint ? <div className="muted small">{hint}</div> : null}
      </div>
      <Switch
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        label={teks ?? "Ubah pengaturan"}
      />
    </div>
  );
}

export { styles as switchStyles };
