import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Token, reset/utilitas, font Plus Jakarta Sans, dan font ikon Phosphor —
// semuanya di-self-host lewat bundel (tanpa CDN produksi).
import { ToastProvider } from "@grahita/ui";

import App from "./App";

// Varian token admin (--h-control 36px, --primary ink, radius kartu 20px).
document.documentElement.dataset.portal = "admin";

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
