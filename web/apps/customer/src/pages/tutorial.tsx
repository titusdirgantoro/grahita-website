import { PageHead } from "@grahita/ui";

import { PanduanLengkap } from "./tutorial-guide";

/** Panduan utama portal cabang; seluruh alur dan gambar berada di satu tempat. */
export function TutorialPage() {
  return (
    <>
      <PageHead title="Tutorial" sub="Panduan langkah demi langkah untuk seluruh alur portal cabang" />
      <PanduanLengkap />
    </>
  );
}
