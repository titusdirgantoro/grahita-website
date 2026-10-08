import type { HTMLAttributes } from "react";

import { cn } from "./cn";

/**
 * Icon merender ikon Phosphor dari font yang di-self-host (bukan CDN).
 *
 * Pemakaian: <Icon name="squares-four" /> → <i class="ph ph-squares-four" />.
 * Ikon selalu `aria-hidden`; makna dibawa oleh teks/label di sekitarnya, sama
 * seperti prototype.
 */
export interface IconProps extends HTMLAttributes<HTMLElement> {
  name: string;
}

export function Icon({ name, className, ...rest }: IconProps) {
  return <i className={cn("ph", `ph-${name}`, className)} aria-hidden="true" {...rest} />;
}
