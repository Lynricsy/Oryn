/**
 * 单个终端的 xterm 视图：把模块级会话里的 xterm 挂进容器；容器变化（`ResizeObserver`，防抖 100ms）与
 * `visualViewport` 变化时重新 fit，属主窗口据此发 `terminal_resize`；标签激活时认领尺寸并聚焦。
 * 非属主窗口按属主的尺寸显示，容器可滚动。等宽 webfont 到位后重新测量并 fit；主题切换时重取配色。
 * 右键菜单：xterm 里有选区时给选区组（解释、引用到输入框、复制…），另有粘贴 / 全选 / 清屏；
 * 程序开了鼠标上报时右键交给程序。触屏长按留给 xterm 自己（不打 `data-ctx-touch`）。
 */
import { type ReactNode, useEffect, useRef } from "react";
import i18n from "../../i18n";
import { useContextTarget } from "../../lib/context-menu/registry";
import { anchorOf } from "../../lib/context-menu/selection";
import { selectionGroups } from "../../lib/context-menu/selection-groups";
import type { MenuGroup } from "../../lib/context-menu/types";
import { host } from "../../lib/host";
import { terminalHub } from "../../state/terminals";
import { THEME_EVENT } from "../../state/theme";
import type { MenuItem } from "../ui/primitives";
import { pasteIntoTerminal } from "./paste";

const FIT_DEBOUNCE_MS = 100;
/** 与 main.tsx 预加载的字体一致。 */
const MONO_FONT = '13px "JetBrains Mono Variable"';

export function TerminalView({
	threadId,
	terminalId,
	autoFocus,
}: {
	threadId: string;
	terminalId: string;
	autoFocus: boolean;
}): ReactNode {
	const ref = useRef<HTMLDivElement | null>(null);

	useEffect(() => {
		const el = ref.current;
		const session = terminalHub.ensure(threadId, terminalId);
		const term = session?.term;
		if (!el || !session || !term) return;
		term.mount?.(el);
		// 会话在视图卸载期间可能错过主题切换：挂载时先对齐一次
		term.retheme?.();
		const refit = () => {
			const size = term.propose?.();
			if (size) terminalHub.fitted(threadId, terminalId, size);
		};
		session.refit = refit;
		// 标签激活：成为属主并按本窗口尺寸显示
		terminalHub.claimSize(threadId, terminalId);
		if (autoFocus) term.focus?.();
		let timer: ReturnType<typeof setTimeout> | undefined;
		const schedule = () => {
			clearTimeout(timer);
			timer = setTimeout(refit, FIT_DEBOUNCE_MS);
		};
		const ro =
			typeof ResizeObserver === "function"
				? new ResizeObserver(schedule)
				: null;
		ro?.observe(el);
		const vv = window.visualViewport;
		vv?.addEventListener("resize", schedule);
		// 等宽字体异步加载：到位后单元格宽度变化，重新测量再 fit
		let disposed = false;
		void document.fonts
			?.load(MONO_FONT)
			.then(() => {
				if (disposed) return;
				term.refont?.();
				schedule();
			})
			.catch(() => {});
		const retheme = () => term.retheme?.();
		window.addEventListener(THEME_EVENT, retheme);
		// 非属主窗口按属主尺寸显示时 xterm 可能比容器小：点容器空白处也聚焦终端
		const focus = () => term.focus?.();
		el.addEventListener("click", focus);
		return () => {
			disposed = true;
			clearTimeout(timer);
			ro?.disconnect();
			vv?.removeEventListener("resize", schedule);
			window.removeEventListener(THEME_EVENT, retheme);
			el.removeEventListener("click", focus);
			if (session.refit === refit) session.refit = null;
		};
	}, [threadId, terminalId, autoFocus]);

	const ctxRef = useContextTarget<HTMLDivElement>({
		// 终端的选区在 xterm 里，不是 DOM 选区：由 items 自己取
		selection: () => null,
		items: (req) => {
			const term = terminalHub.session(threadId, terminalId)?.term;
			if (!term) return null;
			// 终端里的程序开了鼠标上报：右键交给它
			if (term.mouseTracking?.()) return "suppress";
			const text = term.getSelection?.() ?? "";
			const groups: MenuGroup[] = text
				? selectionGroups({
						threadId: req.askThreadId,
						text,
						before: "",
						after: "",
						source: { kind: "terminal" },
						anchor: anchorOf(null, req.x, req.y),
						insert: () => `\`\`\`\n${text}\n\`\`\``,
					})
				: [];
			const items: MenuItem[] = [];
			// Android WebView 读不了剪贴板：粘贴走按键条的输入行
			if (
				host.platform !== "android" &&
				typeof navigator.clipboard?.readText === "function"
			)
				items.push({
					key: "paste",
					icon: "clipboard",
					label: i18n.t("contextMenu.paste"),
					disabled:
						terminalHub.store(threadId).getState().terminals[terminalId]
							?.status !== "live",
					onSelect: () => void pasteIntoTerminal(threadId, terminalId),
				});
			if (term.selectAll)
				items.push({
					key: "selectAll",
					icon: "textT",
					label: i18n.t("contextMenu.selectAll"),
					onSelect: () => term.selectAll?.(),
				});
			if (term.clear)
				items.push({
					key: "clear",
					icon: "trash",
					label: i18n.t("contextMenu.clearTerminal"),
					onSelect: () => term.clear?.(),
				});
			return [...groups, { key: "terminal", items }];
		},
	});

	return (
		<div
			ref={(el) => {
				ref.current = el;
				ctxRef(el);
			}}
			data-testid="terminal-view"
			data-terminal-id={terminalId}
			className="oryn-term h-full w-full overflow-auto"
		/>
	);
}
