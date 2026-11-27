// Excel 工作簿查看器的入口（构建成 dist/sandbox/viewers/xlsx.{js,css}；xlsx 与 xlsm 共用）。

// 渲染视图的 rAF 垫片必须在渲染库求值之前装好
import "../shared/frame-timer-shim";
import { XlsxView } from "../../../src/lib/artifacts/office/XlsxView";
import { mountOffice } from "../shared/office";

mountOffice(XlsxView);
