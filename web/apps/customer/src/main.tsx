import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Menyertakan token, reset/utilitas, font Plus Jakarta Sans, dan font ikon
// Phosphor — semuanya di-self-host lewat bundel (tanpa CDN produksi).
import { ToastProvider } from "@grahita/ui";

import App from "./App";

// Nilai token yang berbeda antar portal (--h-control, --primary, radius, dst.)
// dipilih lewat atribut ini; lihat tokens.css.
document.documentElement.dataset.portal = "customer";

const container = document.getElementById("root");
if (!container) {
  throw new Error("elemen #root tidak ditemukan di index.html");
}

createRoot(container).render(
  <StrictMode>
    <ToastProvider>
      <App />
    </ToastProvider>
  </StrictMode>,
);
