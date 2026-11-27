// Office 预览组件的公共类型（契约 C4）：沙箱里的查看器与应用的文件面板共用同一套组件，组件不依赖应用状态与应用 i18n，
// 文案与资源地址都由调用方传入。评论锚点类型与帧运行时的消息校验共用一份定义（sandbox/runtime/messages.ts）。

import type {
	AnchorPin,
	ViewerAnchor,
} from "../../../../sandbox/runtime/messages";

export type { AnchorPin, ViewerAnchor };

/** 运行时按需加载的静态资源目录，都是以 `/` 结尾的绝对地址。 */
export interface OfficeAssets {
	/** `@silurus/ooxml` 的产物目录（`<格式>.mjs`、解析器 wasm、渲染 worker）。 */
	ooxml: string;
	/** pdf.js 的 worker、CMap、标准字体等资源目录。 */
	pdfjs: string;
}

/** 视图：普通查看、缩略图（只画第一页并铺满宽度）、打印（全部页逐页排开）。 */
export type OfficeView = "full" | "thumbnail" | "print";

/** 组件界面文案；调用方按自己的语言给出。 */
export interface OfficeStrings {
	loading: string;
	/** 文件超过预览上限。 */
	tooLarge: string;
	failed(reason: string): string;
	/** 写进视觉隐藏的状态元素：渲染完成的标志（截图与探针据此判断）。 */
	rendered(pages: number): string;
	pageFailed(n: number): string;
	/** 打印视图页数超过上限。 */
	tooManyPages: string;
	/** 表格没有打印视图。 */
	sheetNoPrint: string;
	pageLabel(n: number): string;
	slideLabel(n: number): string;
	/**
	 * 评论模式下页 / 张上点选按钮的无障碍名称（「在第 N 页添加评论」）。只有会开评论模式的调用方（沙箱查看器）
	 * 需要给；没给时按钮用 pageLabel / slideLabel。
	 */
	commentPage?(n: number): string;
	commentSlide?(n: number): string;
	pageNumber: string;
	zoomIn: string;
	zoomOut: string;
	fitWidth: string;
	present: string;
	exitHint: string;
	prev: string;
	next: string;
	counter(n: number, total: number): string;
}

export interface OfficeViewProps {
	/** 文件内容；组件不修改它（交给库时传副本）。 */
	bytes: ArrayBuffer;
	view: OfficeView;
	assets: OfficeAssets;
	strings: OfficeStrings;
	/** 评论模式下点选页面给出锚点（步骤 7.4 实现）。 */
	pickable?: boolean;
	onPick?(anchor: ViewerAnchor, at: { x: number; y: number }): void;
	/** 要画在页面上的评论图钉（打印与缩略图视图不画）。 */
	pins?: readonly AnchorPin[];
	/** 要滚动定位到的锚点。 */
	locate?: ViewerAnchor | null;
	/** 渲染完成（全部视图的首屏 / 打印视图的全部页）时回调，参数是总页数（张数、工作表数）。 */
	onReady?(pages: number): void;
}
