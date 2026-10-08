import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "@grahita/ui/tokens.css";
import "@grahita/ui/base.css";

import App from "./App";

const container = document.getElementById("root");
if (!container) {
  throw new Error("elemen #root tidak ditemukan di index.html");
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
