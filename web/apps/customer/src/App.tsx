import { RouterProvider } from "@tanstack/react-router";

import { router } from "./routes";

/**
 * App Customer Portal memasang router TanStack.
 *
 * Shell responsif (sidebar + bottom-nav) dan seluruh komponen sistem desain
 * berasal dari @grahita/ui (kartu F1-03).
 */
export default function App() {
  return <RouterProvider router={router} />;
}
