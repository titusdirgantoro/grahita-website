/// <reference path="./assets.d.ts" />
/**
 * @grahita/ui — sistem desain bersama kedua portal (kartu F1-03).
 *
 * Berisi:
 *  - token desain + reset/utilitas (tokens.css, base.css),
 *  - font Plus Jakarta Sans & ikon Phosphor yang di-self-host (impor di bawah;
 *    produksi tidak boleh memuat font/ikon dari CDN — Architecture.md §3.2),
 *  - komponen hasil port prototype: tombol, form, kartu, tabel, chip/badge,
 *    modal, notice, segmented, switch, avatar, state kosong, skeleton, toast,
 *    layout auth,
 *  - shell responsif dua portal: CustomerShell (sidebar + bottom-nav) dan
 *    AdminShell (sidebar ciut + drawer), beserta model menu & guard peran.
 */

import "@fontsource-variable/plus-jakarta-sans";
import "@phosphor-icons/web/regular";

import "./tokens.css";
import "./base.css";

export { cn } from "./cn";

/** Nama token brand yang dijamin tersedia di kedua portal. */
export const NAMA_TOKEN = {
  primary: "--brand-primary",
  secondary: "--brand-secondary",
  accent: "--brand-accent",
  ink: "--ink",
  surface: "--surface",
  line: "--line",
  text: "--text",
  textMuted: "--text-muted",
} as const;

export { Icon, type IconProps } from "./icon";
export {
  Button,
  ButtonLink,
  type ButtonProps,
  type ButtonLinkProps,
  type ButtonVariant,
} from "./components/Button";
export { Field, Input, Textarea, Select, Form, useFieldId, type FieldProps } from "./components/Field";
export { Card, CardHead, type CardProps, type CardHeadProps } from "./components/Card";
export { Badge, Chip, PillNote, type BadgeProps, type BadgeTone, type ChipProps } from "./components/Badge";
export { Notice, type NoticeProps, type NoticeTone } from "./components/Notice";
export { Segmented, type SegmentedOption, type SegmentedProps } from "./components/Segmented";
export { Switch, SwitchRow, type SwitchProps, type SwitchRowProps } from "./components/Switch";
export { Avatar, AvatarLink, initials, type AvatarProps, type AvatarLinkProps } from "./components/Avatar";
export { Brand, type BrandProps } from "./components/Brand";
export { Logo, type LogoProps, type LogoVariant } from "./components/Logo";
export {
  ChangePasswordForm,
  type ChangePasswordFormProps,
} from "./components/ChangePassword";
export {
  PasswordResetPage,
  type PasswordResetPageProps,
} from "./components/PasswordReset";
export {
  MIN_PANJANG,
  periksaKataSandiBaru,
  kekuatanKataSandi,
  pesanGalatSingkat,
  fieldGalat,
  type GalatKataSandi,
  type GalatApiMirip,
  type SkorKekuatan,
  type KekuatanKataSandi,
} from "./password";
export { PasswordStrength, type PasswordStrengthProps } from "./components/PasswordStrength";
export { Table, type TableProps, type TableStack } from "./components/Table";
export { Modal, type ModalProps } from "./components/Modal";
export { EmptyState, type EmptyStateProps } from "./components/EmptyState";
export { Skeleton, SkeletonList, type SkeletonProps, type SkeletonListProps } from "./components/Skeleton";
export { PageHead, type PageHeadProps } from "./components/PageHead";
export { ToastProvider, useToast } from "./components/Toast";
export { AuthLayout, AuthLinks, type AuthLayoutProps } from "./components/AuthLayout";
export { Accordion, type AccordionProps } from "./components/Accordion";

export {
  bolehLihat,
  filterNav,
  filterGroups,
  isActive,
  labelPeran,
  DefaultShellLink,
  type Role,
  type NavItem,
  type NavGroup,
  type ShellLink,
  type ShellLinkProps,
} from "./shell/nav";
export { CustomerShell, type CustomerShellProps, type CustomerShellAccount } from "./shell/CustomerShell";
export { AdminShell, type AdminShellProps, type AdminShellAccount } from "./shell/AdminShell";
