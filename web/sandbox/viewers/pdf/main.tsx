// PDF 查看器的入口（构建成 dist/sandbox/viewers/pdf.{js,css}）。

// 渲染视图的 rAF 垫片必须在渲染库求值之前装好
import "../shared/frame-timer-shim";
import { PdfView } from "../../../src/lib/artifacts/office/PdfView";
import { mountOffice } from "../shared/office";

mountOffice(PdfView);
