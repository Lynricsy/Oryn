// 测试夹具：覆盖八种版式的一份幻灯片（查看器与导出的测试共用），以及导出测试用的最小 PNG。

import type { Deck } from "./slides";

export const EIGHT_LAYOUT_DECK: Deck = {
	title: "季度回顾",
	theme: "light",
	aspect: "16:9",
	slides: [
		{
			layout: "title",
			title: "2026 年第三季度回顾",
			subtitle: "产品组 · 10 月",
			notes: "开场先感谢大家。",
		},
		{
			layout: "bullets",
			title: "本季重点",
			bullets: [
				"发布 2.0",
				{ text: "性能", bullets: ["首屏 -40%", "内存 -25%"] },
			],
			image: "images/launch.png",
			alt: "发布会现场",
		},
		{ layout: "section", title: "第二部分：增长" },
		{
			layout: "two-column",
			title: "方案对比",
			left: { heading: "方案 A", bullets: ["便宜", "慢"] },
			right: { heading: "方案 B", text: "贵一些，但快三倍。" },
		},
		{
			layout: "image",
			image: "images/chart.png",
			alt: "月活走势图",
			title: "月活",
			caption: "数据截至 9 月 30 日",
		},
		{ layout: "quote", quote: "先做对，再做快。", attribution: "团队守则" },
		{
			layout: "stats",
			title: "核心指标",
			stats: [
				{ value: "42%", label: "留存" },
				{ value: "1.2M", label: "月活" },
			],
		},
		{
			layout: "table",
			title: "排期",
			columns: ["阶段", "时间"],
			rows: [
				["设计", "10 月"],
				["开发", "11 月"],
			],
		},
	],
};

/** 只有文件头的 PNG（IHDR 里写宽高）：够读尺寸与嵌入 Word / PowerPoint，导出的测试用。 */
export function tinyPng(width: number, height: number): Uint8Array {
	const b = new Uint8Array(33);
	b.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13]);
	b.set([0x49, 0x48, 0x44, 0x52], 12);
	const dv = new DataView(b.buffer);
	dv.setUint32(16, width);
	dv.setUint32(20, height);
	b.set([8, 6, 0, 0, 0], 24);
	return b;
}
