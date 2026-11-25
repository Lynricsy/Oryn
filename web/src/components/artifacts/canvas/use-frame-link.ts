/**
 * 画布上一个画框的帧通道（静态帧与设计画布的实时帧共用）：帧挂上后建 `FrameLink`，登记到编辑器（`onLink`），
 * 把选择 / 评论模式与评论图钉同步下去，并把 `ready` 与每次加载完成交给调用方（失效检测、实时帧的超时退回）。
 */
import { type RefObject, useEffect, useRef } from "react";
import type {
	SelectMode,
	SelectPin,
} from "../../../../sandbox/runtime/messages";
import { FrameLink } from "../frame-link";

export interface FrameLinkOptions {
	pageId: string;
	/** `postMessage` 的目标源；缺省 `*`（不透明源的 artifact 帧）。 */
	origin?: string;
	mode: SelectMode;
	accent: string;
	pins: SelectPin[];
	/** 画框的通道建立 / 拆除（null）。 */
	onLink(pageId: string, link: FrameLink | null): void;
	onReady(): void;
	/** 帧每次加载完成（`hello` 已发出之后）。 */
	onLoaded?(): void;
}

export function useFrameLink(
	ref: RefObject<HTMLIFrameElement | null>,
	{ pageId, origin = "*", mode, accent, pins, ...cbs }: FrameLinkOptions,
): void {
	const linkRef = useRef<FrameLink | null>(null);
	const cb = useRef(cbs);
	cb.current = cbs;

	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const link = new FrameLink(el, origin);
		linkRef.current = link;
		const offs = [
			link.on("ready", () => cb.current.onReady()),
			link.onLoaded(() => cb.current.onLoaded?.()),
		];
		cb.current.onLink(pageId, link);
		return () => {
			for (const off of offs) off();
			cb.current.onLink(pageId, null);
			linkRef.current = null;
			link.dispose();
		};
	}, [ref, pageId, origin]);

	useEffect(() => {
		linkRef.current?.post({ kind: "select-mode", mode, accent });
	}, [mode, accent]);

	// 调用方每次渲染都给新数组：按内容比较，没变就不重发
	const pinsKey = JSON.stringify(pins);
	useEffect(() => {
		linkRef.current?.post({
			kind: "select-pins",
			pins: JSON.parse(pinsKey) as SelectPin[],
		});
	}, [pinsKey]);
}
