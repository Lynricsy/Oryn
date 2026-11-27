// Excel 工作簿查看器的入口（构建成 dist/sandbox/viewers/xlsx.{js,css}；xlsx 与 xlsm 共用）。

import { XlsxView } from "../../../src/lib/artifacts/office/XlsxView";
import { mountOffice } from "../shared/office";

mountOffice(XlsxView);
