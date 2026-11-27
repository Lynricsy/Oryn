// 四种 Office 视图共用的加载状态、占位与「渲染完成」标志。

import { type ReactNode, useEffect, useRef, useState } from "react";
import { MAX_PREVIEW_BYTES } from "./limits";
import type { OfficeStrings } from "./types";

export type Loaded<T> =
	| { kind: "loading" }
	| { kind: "too-large" }
	| { kind: "error"; reason: string }
	| { kind: "ready"; value: T };

/** 把异常整理成给人看的原因（库的 OoxmlError 带 message）。 */
export function reasonOf(e: unknown): string {
	if (e instanceof Error) return e.message || e.name;
	return String(e);
}

/**
 * 按 bytes 打开文档：超过 MAX_PREVIEW_BYTES 直接给 too-large 不解析；bytes 变化或卸载时用 close 释放。
 * open / close 取最新一次渲染的版本，不进依赖。
 */
export function useLoaded<T>(
	bytes: ArrayBuffer,
	open: (bytes: ArrayBuffer) => Promise<T>,
	close: (value: T) => void,
): Loaded<T> {
	const tooLarge = bytes.byteLength > MAX_PREVIEW_BYTES;
	const [state, setState] = useState<Loaded<T>>(
		tooLarge ? { kind: "too-large" } : { kind: "loading" },
	);
	const fns = useRef({ open, close });
	fns.current = { open, close };

	useEffect(() => {
		if (bytes.byteLength > MAX_PREVIEW_BYTES) {
			setState({ kind: "too-large" });
			return;
		}
		let alive = true;
		let value: { v: T } | null = null;
		setState({ kind: "loading" });
		fns.current.open(bytes).then(
			(v) => {
				if (!alive) {
					fns.current.close(v);
					return;
				}
				value = { v };
				setState({ kind: "ready", value: v });
			},
			(e: unknown) => {
				if (alive) setState({ kind: "error", reason: reasonOf(e) });
			},
		);
		return () => {
			alive = false;
			if (value) fns.current.close(value.v);
		};
	}, [bytes]);

	return tooLarge ? { kind: "too-large" } : state;
}

/** 加载中、过大、失败三种占位。 */
export function OfficeState({
	state,
	strings,
}: {
	state: Exclude<Loaded<unknown>, { kind: "ready" }>;
	strings: OfficeStrings;
}): ReactNode {
	if (state.kind === "loading")
		return (
			<div className="oryn-o-state" aria-busy="true">
				{strings.loading}
			</div>
		);
	if (state.kind === "too-large")
		return <div className="oryn-o-state">{strings.tooLarge}</div>;
	return (
		<div className="oryn-o-state oryn-o-error" role="alert">
			{strings.failed(state.reason)}
		</div>
	);
}

/** 渲染完成后写一行视觉隐藏的「已渲染 N 页」并回调 onReady（每次挂载只回调一次）。 */
export function ReadyMark({
	pages,
	strings,
	onReady,
}: {
	pages: number;
	strings: OfficeStrings;
	onReady?: (pages: number) => void;
}): ReactNode {
	const ready = useRef(onReady);
	ready.current = onReady;
	useEffect(() => {
		ready.current?.(pages);
	}, [pages]);
	return (
		<p className="oryn-o-sr" role="status">
			{strings.rendered(pages)}
		</p>
	);
}

/** 打印视图的纸张尺寸：取首页大小、无边距（Chromium 打印 PDF 时一页一张）。 */
export function PrintPageStyle({ w, h }: { w: number; h: number }): ReactNode {
	return (
		<style>{`@page{size:${Math.round(w)}px ${Math.round(h)}px;margin:0}html,body{background:#fff}`}</style>
	);
}
