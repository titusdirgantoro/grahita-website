/**
 * cn — penggabung kelas nama tanpa dependensi tambahan.
 */
export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}
