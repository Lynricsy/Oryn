// Word 文档（docx / docm）：`@silurus/ooxml` 的 DocxDocument 排版后逐页画到 canvas（见 paged.tsx）。

import type { DocxDocument } from "@silurus/ooxml/docx";
import { type ReactNode, useMemo } from "react";
import { loadDocx } from "./ooxml";
import { PagedView, type PageSource } from "./paged";
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

function DocxPages({
	doc,
	view,
	strings,
	onReady,
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
	return (
		<div className="oryn-o oryn-o-docx">
			<PagedView
				source={source}
				view={view}
				strings={strings}
				label={(i) => strings.pageLabel(i + 1)}
				onReady={onReady}
			/>
		</div>
	);
}
