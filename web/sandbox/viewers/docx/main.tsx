// Word 文档查看器的入口（构建成 dist/sandbox/viewers/docx.{js,css}；docx 与 docm 共用）。

import { DocxView } from "../../../src/lib/artifacts/office/DocxView";
import { mountOffice } from "../shared/office";

mountOffice(DocxView);
