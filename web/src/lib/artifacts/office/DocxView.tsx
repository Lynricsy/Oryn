// Word 文档（docx / docm）：`@silurus/ooxml` 的 DocxDocument 排版后逐页画到 canvas（见 paged.tsx）。
// 评论模式下点页面给出页号与归一化坐标，文字取点中的那一段（库给出的文字行位置命中测试）。

import type { DocxDocument } from "@silurus/ooxml/docx";
import { type ReactNode, useMemo } from "react";
import { clipText, NO_PINS, type Point, pointAnchor } from "./anchor";
import { loadDocx } from "./ooxml";
import { type PageComments, PagedView, type PageSource } from "./paged";
import { OfficeState, useLoaded } from "./shared";
import type { OfficeViewProps } from "./types";
import "./office.css";

/** 1pt = 4/3 CSS px。 */
const PT_TO_PX = 96 / 72;

export function DocxView(props: OfficeViewProps): ReactNode {
	const base = props.assets.ooxml;
	const state = useLoaded<DocxDocument>(
		props.bytes,
		async (bytes) => {
			const m = await loadDocx(base);
			// 库会把缓冲区转移给解析 worker：传副本，调用方的 bytes 保持可用
			return m.DocxDocument.load(bytes.slice(0));
		},
		(doc) => doc.destroy(),
	);
	if (state.kind !== "ready")
		return <OfficeState state={state} strings={props.strings} />;
	return <DocxPages doc={state.value} {...props} />;
}

/**
 * 第 i 页一点下面的文字：命中某个文字行片段时取它所在的整段；没命中文字时看是不是图片、图表、形状里的文字；
 * 都没有回空串。库出错（排版未完成等）也回空串，不影响点选本身。
 */
async function textAt(doc: DocxDocument, i: number, p: Point): Promise<string> {
	try {
		const { widthPt, heightPt } = doc.pageSize(i);
		const w = widthPt * PT_TO_PX;
		const x = p.x * w;
		const y = p.y * heightPt * PT_TO_PX;
		const runs = await doc.collectPageRuns(i, { width: w });
		const hit = runs.find(
			(r) => x >= r.x && x <= r.x + r.w && y >= r.y && y <= r.y + r.h,
		);
		if (hit) {
			const para =
				hit.paragraphId === undefined
					? [hit]
					: runs.filter((r) => r.paragraphId === hit.paragraphId);
			return clipText(para.map((r) => r.text).join(""));
		}
		const element = await doc.getElementContextAt(
			i,
			{ xPt: p.x * widthPt, yPt: p.y * heightPt },
			{ maxTextCharacters: 200 },
		);
		return clipText(element?.text ?? "");
	} catch {
		return "";
	}
}

function DocxPages({
	doc,
	view,
	strings,
	onReady,
	pickable,
	onPick,
	pins,
	locate,
}: OfficeViewProps & { doc: DocxDocument }): ReactNode {
	const source = useMemo<PageSource>(
		() => ({
			count: doc.pageCount,
			size: (i) => {
				const s = doc.pageSize(i);
				return { w: s.widthPt * PT_TO_PX, h: s.heightPt * PT_TO_PX };
			},
			render: (canvas, i, width, dpr) =>
				doc.renderPage(canvas, i, { width, dpr }),
		}),
		[doc],
	);
	const comments = useMemo<PageComments>(
		() => ({
			onPick:
				pickable && onPick
					? (i, point, at) => {
							void textAt(doc, i, point).then((text) =>
								onPick(
									pointAnchor({ number: i + 1, name: "" }, point, text),
									at,
								),
							);
						}
					: null,
			pickLabel: (i) =>
				strings.commentPage?.(i + 1) ?? strings.pageLabel(i + 1),
			pins: pins ?? NO_PINS,
			locate: locate ?? null,
		}),
		[doc, pickable, onPick, pins, locate, strings],
	);
	return (
		<div className="oryn-o oryn-o-docx">
			<PagedView
				source={source}
				view={view}
				strings={strings}
				label={(i) => strings.pageLabel(i + 1)}
				onReady={onReady}
				comments={comments}
			/>
		</div>
	);
}
