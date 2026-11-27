// 查看器界面的两套文案（zh-CN / en）。查看器在沙箱里运行，不走应用的 i18n。

import type { OfficeStrings } from "../../../src/lib/artifacts/office/types";
import type { DeckIssue, DeckProblem } from "../../../src/lib/artifacts/slides";
import type { ViewerLocale } from "./config";

export interface ViewerStrings {
	loading: string;
	loadFailed: string;
	edit: string;
	discard: string;
	saveAs(n: number): string;
	saving: string;
	saved(n: number): string;
	conflict: string;
	notSaved(reason: string): string;
	dismiss: string;
	documentHint: string;
	editBlock: string;
	/** 评论模式下：块上的点选按钮、整篇评论按钮、幻灯片上的点选按钮。 */
	commentBlock: string;
	commentDocument: string;
	commentSlide(n: number): string;
	slidesHint: string;
	prev: string;
	next: string;
	present: string;
	slidesNav: string;
	exitHint: string;
	counter(n: number, total: number): string;
	slideLabel(n: number): string;
	badDeck: string;
	problem(p: DeckProblem): string;
	/** 下载原件（发布页与顶层帧右上角）。 */
	download: string;
	office: OfficeStrings;
}

const ZH_ISSUES: Record<DeckIssue, (limit?: number) => string> = {
	json: () => "不是合法的 JSON",
	object: () => "应该是一个对象",
	string: () => "应该是字符串",
	array: () => "应该是数组",
	empty: () => "不能为空",
	too_long: (n) => `超过 ${n} 个字符`,
	too_few: (n) => `至少要有 ${n} 项`,
	too_many: (n) => `超过 ${n} 项`,
	unknown: () => "未知字段",
	required: () => "缺少必填字段",
	layout: () => "未知的版式",
	enum: () => "取值不合法",
	cells: (n) => `每行应有 ${n} 格，与 columns 一致`,
	column_empty: () => "至少要有 heading、text、bullets 之一",
};

const EN_ISSUES: Record<DeckIssue, (limit?: number) => string> = {
	json: () => "is not valid JSON",
	object: () => "should be an object",
	string: () => "should be a string",
	array: () => "should be an array",
	empty: () => "must not be empty",
	too_long: (n) => `is longer than ${n} characters`,
	too_few: (n) => `needs at least ${n} items`,
	too_many: (n) => `has more than ${n} items`,
	unknown: () => "is not a known field",
	required: () => "is required",
	layout: () => "is not a known layout",
	enum: () => "has an unsupported value",
	cells: (n) => `should have ${n} cells, one per column`,
	column_empty: () => "needs a heading, text or bullets",
};

const ZH: ViewerStrings = {
	loading: "正在加载…",
	loadFailed: "无法读取这个 artifact 的内容。",
	edit: "编辑",
	discard: "放弃",
	saveAs: (n) => `保存为 v${n}`,
	saving: "正在保存…",
	saved: (n) => `已保存为 v${n}`,
	conflict: "已有更新的版本，现在显示的是新版本",
	notSaved: (reason) => `没有保存。${reason}`,
	dismiss: "关闭",
	documentHint: "点击段落、标题或列表来修改文字",
	editBlock: "修改这一块",
	commentBlock: "评论这一块",
	commentDocument: "评论整篇文档",
	commentSlide: (n) => `在第 ${n} 张添加评论`,
	slidesHint: "点击幻灯片上的文字直接修改",
	prev: "上一张",
	next: "下一张",
	present: "演示",
	slidesNav: "翻页",
	exitHint: "按 Esc 退出",
	counter: (n, total) => `${n} / ${total}`,
	slideLabel: (n) => `第 ${n} 张`,
	badDeck: "这份幻灯片的格式有问题，无法显示：",
	problem: (p) => `${p.where || "slides.json"}：${ZH_ISSUES[p.issue](p.limit)}`,
	download: "下载原件",
	office: {
		loading: "正在加载…",
		tooLarge: "文件过大，无法预览",
		failed: (reason) => `无法显示这个文件：${reason}`,
		rendered: (n) => `已渲染 ${n} 页`,
		pageFailed: (n) => `第 ${n} 页无法显示`,
		tooManyPages: "页数过多，无法打印",
		sheetNoPrint: "表格不支持打印视图",
		pageLabel: (n) => `第 ${n} 页`,
		slideLabel: (n) => `第 ${n} 张`,
		commentPage: (n) => `在第 ${n} 页添加评论`,
		commentSlide: (n) => `在第 ${n} 张添加评论`,
		pageNumber: "页码",
		zoomIn: "放大",
		zoomOut: "缩小",
		fitWidth: "适应宽度",
		present: "演示",
		exitHint: "按 Esc 退出",
		prev: "上一张",
		next: "下一张",
		counter: (n, total) => `${n} / ${total}`,
	},
};

const EN: ViewerStrings = {
	loading: "Loading…",
	loadFailed: "Couldn't read this artifact.",
	edit: "Edit",
	discard: "Discard",
	saveAs: (n) => `Save as v${n}`,
	saving: "Saving…",
	saved: (n) => `Saved as v${n}`,
	conflict: "A newer version arrived; you're now looking at it",
	notSaved: (reason) => `Not saved. ${reason}`,
	dismiss: "Dismiss",
	documentHint: "Click a paragraph, heading or list to change its text",
	editBlock: "Edit this block",
	commentBlock: "Comment on this block",
	commentDocument: "Comment on the whole document",
	commentSlide: (n) => `Comment on slide ${n}`,
	slidesHint: "Click any text on the slide to change it",
	prev: "Previous slide",
	next: "Next slide",
	present: "Present",
	slidesNav: "Slide navigation",
	exitHint: "Press Esc to exit",
	counter: (n, total) => `${n} / ${total}`,
	slideLabel: (n) => `Slide ${n}`,
	badDeck: "This deck has format problems and can't be shown:",
	problem: (p) => `${p.where || "slides.json"}: ${EN_ISSUES[p.issue](p.limit)}`,
	download: "Download original",
	office: {
		loading: "Loading…",
		tooLarge: "This file is too large to preview",
		failed: (reason) => `Couldn't display this file: ${reason}`,
		rendered: (n) => `Rendered ${n} ${n === 1 ? "page" : "pages"}`,
		pageFailed: (n) => `Page ${n} couldn't be displayed`,
		tooManyPages: "Too many pages to print",
		sheetNoPrint: "Spreadsheets have no print view",
		pageLabel: (n) => `Page ${n}`,
		slideLabel: (n) => `Slide ${n}`,
		commentPage: (n) => `Comment on page ${n}`,
		commentSlide: (n) => `Comment on slide ${n}`,
		pageNumber: "Page number",
		zoomIn: "Zoom in",
		zoomOut: "Zoom out",
		fitWidth: "Fit width",
		present: "Present",
		exitHint: "Press Esc to exit",
		prev: "Previous slide",
		next: "Next slide",
		counter: (n, total) => `${n} / ${total}`,
	},
};

export function viewerStrings(locale: ViewerLocale): ViewerStrings {
	return locale === "zh-CN" ? ZH : EN;
}
