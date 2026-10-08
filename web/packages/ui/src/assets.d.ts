/*
 * Deklarasi modul untuk aset yang diimpor hanya demi efek samping (CSS).
 *
 * `@phosphor-icons/web/regular` mengekspor berkas `src/regular/style.css`
 * (font ikon Phosphor self-hosted). TypeScript tidak memiliki deklarasi untuk
 * ekspor CSS tersebut, jadi modulnya dideklarasikan di sini; Vite tetap
 * menyelesaikannya lewat peta `exports` paket.
 */
declare module "@phosphor-icons/web/regular";
