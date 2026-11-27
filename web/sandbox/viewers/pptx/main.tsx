// PowerPoint 演示文稿查看器的入口（构建成 dist/sandbox/viewers/pptx.{js,css}；pptx 与 pptm 共用）。
// 演示时经运行时请父页面把帧铺满窗口（没有运行时的顶层帧与发布页里只在页内铺满）。

// 渲染视图的 rAF 垫片必须在渲染库求值之前装好
import "../shared/frame-timer-shim";
import type { ReactNode } from "react";
import { PptxView } from "../../../src/lib/artifacts/office/PptxView";
import type { OfficeViewProps } from "../../../src/lib/artifacts/office/types";
import { mountOffice, requestPresent } from "../shared/office";

function PresentingPptx(props: OfficeViewProps): ReactNode {
	return <PptxView {...props} onPresent={requestPresent} />;
}

mountOffice(PresentingPptx);
