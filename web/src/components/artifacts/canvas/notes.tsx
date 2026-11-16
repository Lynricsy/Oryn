/**
 * 画布标注的 SVG 渲染（计划 M14 6.3）：文字、便签、画笔 / 荧光笔、矩形、椭圆、线、箭头。
 * 画在画框之上的一层，世界坐标；颜色经 `c-<颜色>` 类取 `--c`（见 canvas.css）。
 */
import type { ReactNode } from "react";
import {
	type CanvasDoc,
	type CanvasNote,
	type CanvasPage,
	joinedEnds,
	noteBox,
} from "../../../lib/artifacts/canvas-doc";
import { strokePath } from "../../../lib/artifacts/stroke";

/** 箭头的箭头部分：从终点往回、左右各张开 28°。 */
export function arrowHead(
	x1: number,
	y1: number,
	x2: number,
	y2: number,
	width: number,
): string {
	const len = Math.max(10, width * 4);
	const angle = Math.atan2(y2 - y1, x2 - x1);
	const spread = (28 * Math.PI) / 180;
	const ax = x2 - len * Math.cos(angle - spread);
	const ay = y2 - len * Math.sin(angle - spread);
	const bx = x2 - len * Math.cos(angle + spread);
	const by = y2 - len * Math.sin(angle + spread);
	return `M${ax} ${ay}L${x2} ${y2}L${bx} ${by}`;
}

/** 一条标注。`hidden` 时不画（正在就地编辑它的文字）。 */
export function NoteShape({
	note,
	pages,
	hideText = false,
}: {
	note: CanvasNote;
	pages: readonly CanvasPage[];
	hideText?: boolean;
}): ReactNode {
	const cls = `canvas-note c-${note.color}${note.kind === "sticky" && note.done ? " done" : ""}`;
	switch (note.kind) {
		case "text":
			return (
				<g className={cls} data-note={note.id}>
					{!hideText && (
						<text className="text" x={note.x} y={note.y} fontSize={note.size}>
							{note.text.split("\n").map((line, i) => (
								<tspan
									// 行号就是行的身份
									// biome-ignore lint/suspicious/noArrayIndexKey: 行没有别的标识
									key={i}
									x={note.x}
									dy={i === 0 ? note.size : note.size * 1.3}
								>
									{line}
								</tspan>
							))}
						</text>
					)}
				</g>
			);
		case "sticky": {
			const b = noteBox(note);
			return (
				<g className={cls} data-note={note.id}>
					<rect
						className="sticky"
						x={b.x}
						y={b.y}
						width={b.width}
						height={b.height}
						rx={6}
					/>
					{!hideText && (
						<foreignObject x={b.x} y={b.y} width={b.width} height={b.height}>
							<div className="sticky-text">{note.text}</div>
						</foreignObject>
					)}
				</g>
			);
		}
		case "pen":
			return (
				<g className={cls} data-note={note.id}>
					<path
						className={note.marker ? "line marker" : "line"}
						d={strokePath(note.points)}
						strokeWidth={note.width}
					/>
				</g>
			);
		case "rect":
		case "ellipse": {
			const b = noteBox(note);
			return (
				<g className={cls} data-note={note.id}>
					{note.kind === "rect" ? (
						<rect
							className="line"
							x={b.x}
							y={b.y}
							width={b.width}
							height={b.height}
							strokeWidth={note.width}
						/>
					) : (
						<ellipse
							className="line"
							cx={b.x + b.width / 2}
							cy={b.y + b.height / 2}
							rx={b.width / 2}
							ry={b.height / 2}
							strokeWidth={note.width}
						/>
					)}
				</g>
			);
		}
		default: {
			const { x1, y1, x2, y2 } = joinedEnds(note, pages);
			let d = `M${x1} ${y1}L${x2} ${y2}`;
			if (note.kind === "arrow") d += arrowHead(x1, y1, x2, y2, note.width);
			return (
				<g className={cls} data-note={note.id}>
					<path className="line" d={d} strokeWidth={note.width} />
				</g>
			);
		}
	}
}

/** 某个画布页上的全部标注（绘制顺序）。 */
export function NotesLayer({
	doc,
	canvas,
	editing,
	draft,
}: {
	doc: CanvasDoc;
	canvas: string;
	/** 正在就地编辑文字的标注：只画底，不画字。 */
	editing: string | null;
	/** 正在画的那一笔 / 那个形状（还没提交）。 */
	draft: CanvasNote | null;
}): ReactNode {
	return (
		<svg className="canvas-layer" aria-hidden="true">
			{doc.notes
				.filter((n) => n.canvas === canvas)
				.map((n) => (
					<NoteShape
						key={n.id}
						note={n}
						pages={doc.pages}
						hideText={n.id === editing}
					/>
				))}
			{draft && <NoteShape note={draft} pages={doc.pages} />}
		</svg>
	);
}
