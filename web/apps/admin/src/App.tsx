import { RouterProvider } from "@tanstack/react-router";

import { router } from "./routes";

/**
 * App Admin Portal memasang router TanStack.
 *
 * Shell responsif (sidebar ciut + drawer), sistem desain, dan guard menu peran
 * berasal dari @grahita/ui. Rute admin berada di root host admin
 * (grahita-admin.tiusclaw.com), bukan berprefiks /admin.
 */
export default function App() {
  return <RouterProvider router={router} />;
}
