import {
  cloneElement,
  forwardRef,
  isValidElement,
  useId,
  type FormHTMLAttributes,
  type InputHTMLAttributes,
  type ReactElement,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";

import { cn } from "../cn";
import styles from "./Field.module.css";

export interface FieldProps {
  label: ReactNode;
  /** id elemen input yang dilabeli; wajib agar label terhubung (a11y). */
  htmlFor?: string;
  hint?: ReactNode;
  error?: ReactNode;
  children: ReactNode;
  className?: string;
}

/**
 * Field membungkus label + kontrol + pesan error, meniru `.field`/`.label`/
 * `.err` prototype. Bila `error` ada, kontrol otomatis memakai `aria-invalid`
 * (lihat Input/Textarea/Select yang menerima prop `error`).
 */
export function Field({ label, htmlFor, hint, error, children, className }: FieldProps) {
  // Pesan error di Field otomatis menandai kontrol sebagai tidak valid
  // (aria-invalid) tanpa pemanggil harus meneruskan prop `invalid` dua kali.
  const kontrol =
    error && isValidElement(children)
      ? cloneElement(children as ReactElement<{ invalid?: boolean }>, { invalid: true })
      : children;

  return (
    <div className={cn(styles.field, className)}>
      <label className={styles.label} htmlFor={htmlFor}>
        {label}
      </label>
      {kontrol}
      {hint && !error ? <span className={styles.hint}>{hint}</span> : null}
      {error ? (
        <span className={styles.err} role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}

type InputBase = { invalid?: boolean; inputClassName?: string };

export type InputProps = InputBase &
  Omit<InputHTMLAttributes<HTMLInputElement>, "className">;

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { invalid, inputClassName, ...rest },
  ref,
) {
  return (
    <input
      ref={ref}
      className={cn(styles.input, invalid && styles.invalid, inputClassName)}
      aria-invalid={invalid || undefined}
      {...rest}
    />
  );
});

export type TextareaProps = InputBase &
  Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "className">;

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { invalid, inputClassName, ...rest },
  ref,
) {
  return (
    <textarea
      ref={ref}
      className={cn(styles.input, invalid && styles.invalid, inputClassName)}
      aria-invalid={invalid || undefined}
      {...rest}
    />
  );
});

export type SelectProps = InputBase &
  Omit<SelectHTMLAttributes<HTMLSelectElement>, "className">;

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { invalid, inputClassName, children, ...rest },
  ref,
) {
  return (
    <select
      ref={ref}
      className={cn(styles.input, invalid && styles.invalid, inputClassName)}
      aria-invalid={invalid || undefined}
      {...rest}
    >
      {children}
    </select>
  );
});

/** Form adalah wadah `<form>` dengan jarak `.form-grid` prototype. */
export function Form({
  children,
  className,
  ...rest
}: FormHTMLAttributes<HTMLFormElement> & { children: ReactNode }) {
  return (
    <form className={cn(styles.formGrid, className)} noValidate {...rest}>
      {children}
    </form>
  );
}

/** useFieldId memberi id stabil untuk menghubungkan label dan kontrol. */
export function useFieldId(prefix: string): string {
  const id = useId();
  return `${prefix}-${id}`;
}

export { styles as fieldStyles };
