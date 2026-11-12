/**
 * 触屏设备的终端按键条（只在 `(pointer: coarse)` 时显示）：Esc Tab Ctrl(粘滞) ↑ ↓ ← → / - | ~ 粘贴。
 * 按钮不抢焦点（软键盘保持弹出）；粘滞 Ctrl 作用于下一次按键条或键盘输入。
 */
import {
	type ReactNode,
	useEffect,
	useState,
	useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import { terminalHub } from "../../state/terminals";

const COARSE = "(pointer: coarse)";

function subscribeCoarse(cb: () => void) {
	const mq = window.matchMedia?.(COARSE);
	mq?.addEventListener("change", cb);
	return () => mq?.removeEventListener("change", cb);
}

export function useCoarsePointer(): boolean {
	return useSyncExternalStore(
		subscribeCoarse,
		() => window.matchMedia?.(COARSE).matches ?? false,
		() => false,
	);
}

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

	const send = (k: KeyDef) => {
		if (k.id === "ctrl") {
			setCtrl((v) => !v);
			return;
		}
		let data = k.seq ?? "";
		if (k.arrow) data = `\x1b[${ctrl ? "1;5" : ""}${ARROWS[k.arrow]}`;
		else if (ctrl) data = ctrlChord(data);
		setCtrl(false);
		const s = terminalHub.session(threadId, terminalId);
		// 已经算好控制序列：绕过粘滞改写
		const transform = s?.transform ?? null;
		if (s) s.transform = null;
		terminalHub.input(threadId, terminalId, data);
		if (s) s.transform = transform;
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
		<div
			role="toolbar"
			aria-label={t("terminal.keyBar")}
			className="flex shrink-0 gap-1 overflow-x-auto border-border border-t bg-raised px-1.5 py-1"
		>
			{KEYS.map((k) => (
				<button
					key={k.id}
					type="button"
					aria-pressed={k.id === "ctrl" ? ctrl : undefined}
					className={`h-8 min-w-9 shrink-0 rounded-md border border-border px-2 font-mono text-xs ${k.id === "ctrl" && ctrl ? "bg-accent text-accent-fg" : "bg-panel text-fg"}`}
					onPointerDown={(e) => e.preventDefault()}
					onClick={() => send(k)}
				>
					{k.label}
				</button>
			))}
			<button
				type="button"
				className="h-8 shrink-0 rounded-md border border-border bg-panel px-2 text-xs"
				onPointerDown={(e) => e.preventDefault()}
				onClick={() => void paste()}
			>
				{t("terminal.paste")}
			</button>
		</div>
	);
}
