/**
 * xterm.js 工厂（计划 M3 2.5）：默认 DOM 渲染器（不装 WebGL / canvas 插件），fit / web-links / unicode11 插件；
 * 主题取界面 CSS 变量；`Ctrl+Shift+C/V` 复制粘贴。只由终端面板模块导入，主包不含 xterm。
 */
import { FitAddon } from "@xterm/addon-fit";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { type ITheme, Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { cssColor } from "../../lib/css-color";
import type {
	TermFactory,
	TermHandlers,
	TermLike,
} from "../../state/terminals";

function theme(): ITheme {
	const bg = cssColor("--inset", "#1e1e1e");
	const fg = cssColor("--text", "#e6e6e6");
	return {
		background: bg,
		foreground: fg,
		cursor: cssColor("--brand", "#e0823d"),
		cursorAccent: bg,
		selectionBackground: `${cssColor("--brand", "#e0823d")}55`,
	};
}

class XtermTerm implements TermLike {
	private readonly term: Terminal;
	private readonly fit = new FitAddon();
	private readonly onFocus: () => void;
	private focusBound = false;
	/** 已 write 但 xterm 尚未解析的字符数。 */
	private pending = 0;

	constructor(handlers: TermHandlers) {
		const compact = typeof window !== "undefined" && window.innerWidth < 600;
		this.term = new Terminal({
			scrollback: 5000,
			allowProposedApi: true,
			macOptionIsMeta: true,
			fontSize: compact ? 12 : 13,
			fontFamily:
				'"JetBrains Mono Variable", "Symbols Nerd Font Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace',
			cols: 100,
			rows: 30,
			theme: theme(),
		});
		this.term.loadAddon(this.fit);
		this.term.loadAddon(new WebLinksAddon());
		this.term.loadAddon(new Unicode11Addon());
		this.term.unicode.activeVersion = "11";
		this.term.onData((d) => handlers.onInput(d));
		this.term.onBinary((d) => handlers.onInput(d));
		this.term.attachCustomKeyEventHandler((ev) => {
			if (ev.type !== "keydown" || !ev.ctrlKey || !ev.shiftKey) return true;
			const key = ev.key.toLowerCase();
			if (key === "c") {
				const sel = this.term.getSelection();
				if (sel) void navigator.clipboard?.writeText(sel).catch(() => {});
				return false;
			}
			if (key === "v") {
				void navigator.clipboard
					?.readText()
					.then((text) => this.paste(text))
					.catch(() => {});
				return false;
			}
			return true;
		});
		this.onFocus = handlers.onFocus;
	}

	get cols() {
		return this.term.cols;
	}
	get rows() {
		return this.term.rows;
	}
	write(data: string) {
		this.pending += data.length;
		this.term.write(data, () => {
			this.pending -= data.length;
		});
	}
	reset() {
		// xterm 的 reset 立即生效而写缓冲是异步的：排进写队列，避免积压的旧输出落在复位之后
		this.term.write("", () => this.term.reset());
	}
	backlog() {
		return this.pending;
	}
	resize(cols: number, rows: number) {
		if (cols < 2 || rows < 1) return;
		if (cols === this.term.cols && rows === this.term.rows) return;
		this.term.resize(cols, rows);
	}
	dispose() {
		this.term.dispose();
	}
	paste(text: string) {
		if (text) this.term.paste(text);
	}
	focus() {
		this.term.focus();
	}
	mount(el: HTMLElement) {
		const node = this.term.element;
		if (node) {
			if (node.parentElement !== el) el.appendChild(node);
		} else this.term.open(el);
		if (!this.focusBound && this.term.textarea) {
			this.focusBound = true;
			this.term.textarea.addEventListener("focus", () => this.onFocus());
		}
	}
	propose() {
		if (!this.term.element?.parentElement) return undefined;
		const d = this.fit.proposeDimensions();
		if (!d || !Number.isFinite(d.cols) || !Number.isFinite(d.rows))
			return undefined;
		const cols = Math.max(2, Math.floor(d.cols));
		const rows = Math.max(1, Math.floor(d.rows));
		return { cols, rows };
	}
	text() {
		const buf = this.term.buffer.active;
		const lines: string[] = [];
		for (let i = 0; i < buf.length; i++) {
			const line = buf.getLine(i);
			if (!line) continue;
			const s = line.translateToString(true);
			if (line.isWrapped && lines.length > 0) lines[lines.length - 1] += s;
			else lines.push(s);
		}
		while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
		return lines.join("\n");
	}
}

export const createXterm: TermFactory = (handlers) => new XtermTerm(handlers);
