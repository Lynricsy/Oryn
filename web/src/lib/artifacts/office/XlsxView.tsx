// Excel 工作簿（xlsx / xlsm）：普通视图是 `@silurus/ooxml` 自带的网格与工作表标签（XlsxViewer）；缩略图只画第一个
// 工作表的左上区域（XlsxSheetViewer，无滚动条）；表格没有打印视图。

import type { XlsxSheetViewer, XlsxViewer } from "@silurus/ooxml/xlsx";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { MAX_PREVIEW_BYTES } from "./limits";
import { loadXlsx } from "./ooxml";
import { OfficeState, ReadyMark, reasonOf } from "./shared";
import type { OfficeViewProps } from "./types";
import "./office.css";

export function XlsxView(props: OfficeViewProps): ReactNode {
	if (props.bytes.byteLength > MAX_PREVIEW_BYTES)
		return (
			<OfficeState state={{ kind: "too-large" }} strings={props.strings} />
		);
	if (props.view === "print")
		return <div className="oryn-o-state">{props.strings.sheetNoPrint}</div>;
	return <XlsxSheets {...props} />;
}

type SheetState =
	| { kind: "loading" }
	| { kind: "error"; reason: string }
	| { kind: "ready"; sheets: number };

function XlsxSheets({
	bytes,
	view,
	assets,
	strings,
	onReady,
}: OfficeViewProps): ReactNode {
	const host = useRef<HTMLDivElement>(null);
	const base = assets.ooxml;
	const [state, setState] = useState<SheetState>({ kind: "loading" });

	useEffect(() => {
		const el = host.current;
		if (!el) return;
		let alive = true;
		let viewer: XlsxViewer | XlsxSheetViewer | null = null;
		setState({ kind: "loading" });
		(async () => {
			const m = await loadXlsx(base);
			if (!alive) return 0;
			if (view === "thumbnail") {
				const canvas = document.createElement("canvas");
				canvas.className = "oryn-o-sheet-canvas";
				el.append(canvas);
				const v = new m.XlsxSheetViewer(canvas, {
					showScrollbars: false,
					enableHyperlinks: false,
				});
				viewer = v;
				await v.load(bytes.slice(0));
				return v.sheetCount;
			}
			// 链接不在沙箱里跳转（外链经库的 window.open 打开会被帧策略拦下）
			const v = new m.XlsxViewer(el, { enableHyperlinks: false });
			viewer = v;
			await v.load(bytes.slice(0));
			return v.sheetCount;
		})().then(
			(sheets) => {
				if (alive) setState({ kind: "ready", sheets });
			},
			(e: unknown) => {
				if (alive) setState({ kind: "error", reason: reasonOf(e) });
			},
		);
		return () => {
			alive = false;
			viewer?.destroy();
			el.replaceChildren();
		};
	}, [bytes, view, base]);

	return (
		<div
			className={`oryn-o oryn-o-xlsx${view === "thumbnail" ? " oryn-o-xlsx-thumbnail" : ""}`}
		>
			<div ref={host} className="oryn-o-sheet-host" />
			{state.kind === "ready" ? (
				<ReadyMark pages={state.sheets} strings={strings} onReady={onReady} />
			) : (
				<div className="oryn-o-overlay">
					<OfficeState state={state} strings={strings} />
				</div>
			)}
		</div>
	);
}
