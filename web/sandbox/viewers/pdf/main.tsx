// PDF 查看器的入口（构建成 dist/sandbox/viewers/pdf.{js,css}）。

import { PdfView } from "../../../src/lib/artifacts/office/PdfView";
import { mountOffice } from "../shared/office";

mountOffice(PdfView);
