// Word 文档查看器的入口（构建成 dist/sandbox/viewers/docx.{js,css}；docx 与 docm 共用）。

// 渲染视图的 rAF 垫片必须在渲染库求值之前装好
import "../shared/frame-timer-shim";
import { DocxView } from "../../../src/lib/artifacts/office/DocxView";
import { mountOffice } from "../shared/office";

mountOffice(DocxView);
