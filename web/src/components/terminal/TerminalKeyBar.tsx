/**
 * 触屏设备的终端按键条（只在 `(pointer: coarse)` 时显示）：Esc Tab Ctrl(粘滞) ↑ ↓ ← → / - | ~ 输入行 粘贴。
 * 按钮不抢焦点（软键盘保持弹出）；粘滞 Ctrl 作用于下一次按键条、输入行或键盘输入。
 * 输入行（M7 计划 4.4）：按键条上方的普通文本框，软键盘的联想、语音输入、粘贴都在这里完成，回车整行发出（追加 `\r`）。
 * Android WebView 读剪贴板受限，「粘贴」键改为打开并聚焦输入行，用系统的长按粘贴。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { host } from "../../lib/host";
import { terminalHub } from "../../state/terminals";
import { Icon } from "../ui/icons";
import { TextInput } from "../ui/primitives";

/** 键帽：放大版 Kbd（按钮不抢焦点，靠按下缩放给反馈）。 */
const KEYCAP =
	"inline-flex h-8 shrink-0 items-center justify-center rounded-md border text-xs shadow-xs transition-[background-color,color,scale] duration-150 ease-out active:scale-95";

/** Ctrl+字符 → 控制字符；无对应控制码的原样返回。 */
export function ctrlChord(data: string): string {
	if (data.length !== 1) return data;
	const c = data.toUpperCase().charCodeAt(0);
	if (c >= 64 && c <= 95) return String.fromCharCode(c - 64); // @A–Z[\]^_
	if (data === " ") return "\x00";
	if (data === "?") return "\x7f";
	return data;
}

const ARROWS: Record<string, string> = {
	up: "A",
	down: "B",
	right: "C",
	left: "D",
};

interface KeyDef {
	id: string;
	label: string;
	seq?: string;
	arrow?: keyof typeof ARROWS;
}

const KEYS: KeyDef[] = [
	{ id: "esc", label: "Esc", seq: "\x1b" },
	{ id: "tab", label: "Tab", seq: "\t" },
	{ id: "ctrl", label: "Ctrl" },
	{ id: "up", label: "↑", arrow: "up" },
	{ id: "down", label: "↓", arrow: "down" },
	{ id: "left", label: "←", arrow: "left" },
	{ id: "right", label: "→", arrow: "right" },
	{ id: "slash", label: "/", seq: "/" },
	{ id: "dash", label: "-", seq: "-" },
	{ id: "pipe", label: "|", seq: "|" },
	{ id: "tilde", label: "~", seq: "~" },
];

export function TerminalKeyBar({
	threadId,
	terminalId,
}: {
	threadId: string;
	terminalId: string;
}): ReactNode {
	const { t } = useTranslation();
	const [ctrl, setCtrl] = useState(false);
	const [line, setLine] = useState(false);
	const [value, setValue] = useState("");
	const lineForm = useRef<HTMLFormElement>(null);
	const focusLine = () => lineForm.current?.querySelector("input")?.focus();

	// 粘滞 Ctrl 同样作用于键盘输入：改写下一次输入后自动解除
	useEffect(() => {
		const s = terminalHub.ensure(threadId, terminalId);
		if (!s) return;
		s.transform = ctrl
			? (data) => {
					setCtrl(false);
					return ctrlChord(data);
				}
			: null;
		return () => {
			s.transform = null;
		};
	}, [ctrl, threadId, terminalId]);

	// 打开输入行时聚焦（软键盘随之弹出）
	useEffect(() => {
		if (line) lineForm.current?.querySelector("input")?.focus();
	}, [line]);

	/** 发出已经算好的数据：绕过粘滞改写。 */
	const sendRaw = (data: string) => {
		const s = terminalHub.session(threadId, terminalId);
		const transform = s?.transform ?? null;
		if (s) s.transform = null;
		terminalHub.input(threadId, terminalId, data);
		if (s) s.transform = transform;
	};

	const send = (k: KeyDef) => {
		if (k.id === "ctrl") {
			setCtrl((v) => !v);
			return;
		}
		let data = k.seq ?? "";
		if (k.arrow) data = `\x1b[${ctrl ? "1;5" : ""}${ARROWS[k.arrow]}`;
		else if (ctrl) data = ctrlChord(data);
		setCtrl(false);
		sendRaw(data);
	};

	/** 输入行回车：整行 + `\r`；粘滞 Ctrl 时改写为控制字符、不追加回车。 */
	const submitLine = () => {
		sendRaw(ctrl && value ? ctrlChord(value) : `${value}\r`);
		setCtrl(false);
		setValue("");
		focusLine();
	};

	const openLine = () => {
		if (line) focusLine();
		else setLine(true);
	};

	const paste = async () => {
		try {
			const text = await navigator.clipboard.readText();
			const term = terminalHub.session(threadId, terminalId)?.term;
			if (term?.paste) term.paste(text);
			else terminalHub.input(threadId, terminalId, text);
		} catch {
			// 未授权读取剪贴板
		}
	};

	return (
		<div className="flex shrink-0 flex-col border-border border-t bg-raised">
			{line && (
				<form
					ref={lineForm}
					className="px-2 pt-1.5"
					onSubmit={(e) => {
						e.preventDefault();
						submitLine();
					}}
				>
					<TextInput
						data-testid="terminal-line-input"
						type="text"
						aria-label={t("terminal.lineInput")}
						placeholder={t("terminal.lineInputPlaceholder")}
						enterKeyHint="send"
						autoCapitalize="off"
						autoCorrect="off"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						value={value}
						onChange={(e) => setValue(e.target.value)}
					/>
				</form>
			)}
			<div
				role="toolbar"
				aria-label={t("terminal.keyBar")}
				className="focus-inset flex gap-1 overflow-x-auto px-2 py-1.5"
			>
				{KEYS.map((k) => (
					<button
						key={k.id}
						type="button"
						aria-pressed={k.id === "ctrl" ? ctrl : undefined}
						className={`${KEYCAP} min-w-8 px-2 font-mono ${k.id === "ctrl" && ctrl ? "border-accent/40 bg-accent-soft text-accent-ink" : "border-border bg-panel text-fg"}`}
						onPointerDown={(e) => e.preventDefault()}
						onClick={() => send(k)}
					>
						{k.label}
					</button>
				))}
				<button
					type="button"
					data-testid="terminal-line-toggle"
					aria-pressed={line}
					className={`${KEYCAP} px-2.5 ${line ? "border-accent/40 bg-accent-soft text-accent-ink" : "border-border bg-panel text-fg"}`}
					onPointerDown={(e) => e.preventDefault()}
					onClick={() => setLine((v) => !v)}
				>
					{t("terminal.lineInput")}
				</button>
				<button
					type="button"
					className={`${KEYCAP} gap-1 border-border bg-panel px-2.5 text-fg`}
					onPointerDown={(e) => e.preventDefault()}
					onClick={() =>
						host.platform === "android" ? openLine() : void paste()
					}
				>
					<Icon name="copy" size={13} className="text-faint" />
					{t("terminal.paste")}
				</button>
			</div>
		</div>
	);
}
