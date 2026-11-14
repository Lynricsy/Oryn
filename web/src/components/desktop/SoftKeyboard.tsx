/**
 * 触屏软键盘（M8 4.5）：键盘按钮让一个屏幕外的 `<textarea>` 获得焦点，系统键盘的输入经 `beforeinput` 逐字符转成 keysym 发给桌面。
 * - textarea 内容始终保持为一个空格作哨兵（Android 键盘在空输入框里不发退格），每次事件后复位。
 * - `insertText` → 逐字符；`deleteContentBackward` → BackSpace；`insertLineBreak` / `insertParagraph` → Return；
 *   输入法组合期间不发，`compositionend` 时发最终文本；不可取消的输入由 `input` 事件按差异兜底。
 * - 下方一行附加键：Esc、Tab、Ctrl（粘滞）、Alt（粘滞）、四个方向键；粘滞键在下一个按键之后自动松开。
 *   附加键按下时不抢焦点（`pointerdown` 阻止默认），系统键盘不会收起。
 */
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { KEYSYM, keysymOf } from "./keysym";

const SENTINEL = " ";

type Mod = "ctrl" | "alt";

const MOD_SYM: Record<Mod, number> = {
	ctrl: KEYSYM.Control_L,
	alt: KEYSYM.Alt_L,
};

const ARROWS: Record<string, number> = {
	ArrowLeft: KEYSYM.Left,
	ArrowUp: KEYSYM.Up,
	ArrowRight: KEYSYM.Right,
	ArrowDown: KEYSYM.Down,
	Escape: KEYSYM.Escape,
	Tab: KEYSYM.Tab,
};

export function SoftKeyboard({
	onKey,
}: {
	/** `down` 省略 = 按下再抬起 */
	onKey: (keysym: number, down?: boolean) => void;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLTextAreaElement>(null);
	const [mods, setMods] = useState<Record<Mod, boolean>>({
		ctrl: false,
		alt: false,
	});
	// 事件处理器里读最新的粘滞状态（原生监听只登记一次）
	const modsRef = useRef(mods);
	modsRef.current = mods;
	const onKeyRef = useRef(onKey);
	onKeyRef.current = onKey;

	/** 发一个键：粘滞修饰键先按下、键按下抬起、修饰键逆序抬起，然后松开粘滞。 */
	const pressRef = useRef((sym: number) => {
		const held = (["ctrl", "alt"] as const).filter((m) => modsRef.current[m]);
		for (const m of held) onKeyRef.current(MOD_SYM[m], true);
		onKeyRef.current(sym);
		for (const m of [...held].reverse()) onKeyRef.current(MOD_SYM[m], false);
		if (held.length > 0) {
			modsRef.current = { ctrl: false, alt: false };
			setMods(modsRef.current);
		}
	});

	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const reset = () => {
			el.value = SENTINEL;
			el.setSelectionRange(SENTINEL.length, SENTINEL.length);
		};
		const send = (text: string) => {
			for (const c of text) pressRef.current(keysymOf(c));
		};
		const onBeforeInput = (e: Event) => {
			const ev = e as InputEvent;
			// 不可取消的输入会真的改内容，交给 input 事件按差异发，免得发两次
			if (ev.isComposing || !ev.cancelable) return;
			switch (ev.inputType) {
				// 组合输入的文本只在 compositionend 里发
				case "insertCompositionText":
				case "deleteCompositionText":
				case "insertFromComposition":
					return;
				case "insertText":
					send(ev.data ?? "");
					break;
				case "insertLineBreak":
				case "insertParagraph":
					pressRef.current(KEYSYM.Return);
					break;
				case "deleteContentBackward":
					pressRef.current(KEYSYM.BackSpace);
					break;
				default:
					break;
			}
			ev.preventDefault();
			reset();
		};
		const onCompositionEnd = (e: Event) => {
			send((e as CompositionEvent).data ?? "");
			reset();
		};
		// 不可取消的输入（部分安卓输入法）：按与哨兵的差异补发
		const onInput = (e: Event) => {
			if ((e as InputEvent).isComposing) return;
			const v = el.value;
			if (v === SENTINEL) return;
			if (v.length < SENTINEL.length) pressRef.current(KEYSYM.BackSpace);
			else if (v.startsWith(SENTINEL)) send(v.slice(SENTINEL.length));
			reset();
		};
		reset();
		el.focus({ preventScroll: true });
		el.addEventListener("beforeinput", onBeforeInput);
		el.addEventListener("compositionend", onCompositionEnd);
		el.addEventListener("input", onInput);
		return () => {
			el.removeEventListener("beforeinput", onBeforeInput);
			el.removeEventListener("compositionend", onCompositionEnd);
			el.removeEventListener("input", onInput);
		};
	}, []);

	const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
		const sym = ARROWS[e.key];
		if (sym === undefined || e.nativeEvent.isComposing) return;
		e.preventDefault();
		pressRef.current(sym);
	};

	const keyClass =
		"inline-flex h-8 min-w-9 shrink-0 select-none items-center justify-center rounded-md border border-border bg-raised px-2 font-mono text-muted text-xs transition-colors duration-150 active:bg-active";
	const key = (label: string, sym: number, aria: string) => (
		<button
			key={label}
			type="button"
			aria-label={aria}
			className={keyClass}
			onPointerDown={(e) => e.preventDefault()}
			onClick={() => {
				pressRef.current(sym);
				ref.current?.focus({ preventScroll: true });
			}}
		>
			{label}
		</button>
	);
	const mod = (m: Mod, label: string) => (
		<button
			key={m}
			type="button"
			aria-pressed={mods[m]}
			aria-label={t(`deskPanel.kb.${m}`)}
			className={`${keyClass} ${mods[m] ? "border-border-strong bg-active text-fg" : ""}`}
			onPointerDown={(e) => e.preventDefault()}
			onClick={() => {
				const next = { ...modsRef.current, [m]: !modsRef.current[m] };
				modsRef.current = next;
				setMods(next);
				ref.current?.focus({ preventScroll: true });
			}}
		>
			{label}
		</button>
	);

	return (
		<div
			data-testid="desktop-softkeys"
			className="relative flex shrink-0 items-center gap-1 overflow-x-auto border-border border-t px-2 py-1.5 [scrollbar-width:none]"
		>
			<textarea
				ref={ref}
				data-testid="desktop-kb-input"
				aria-label={t("deskPanel.kb.input")}
				autoCapitalize="off"
				autoCorrect="off"
				autoComplete="off"
				spellCheck={false}
				rows={1}
				// 16px：iOS 不因聚焦小字号输入框而整页放大
				className="pointer-events-none absolute top-0 left-0 h-px w-px resize-none overflow-hidden bg-transparent text-base text-transparent opacity-0 outline-none"
				onKeyDown={onKeyDown}
			/>
			{key("Esc", KEYSYM.Escape, "Esc")}
			{key("Tab", KEYSYM.Tab, "Tab")}
			{mod("ctrl", "Ctrl")}
			{mod("alt", "Alt")}
			{key("←", KEYSYM.Left, t("deskPanel.kb.left"))}
			{key("↑", KEYSYM.Up, t("deskPanel.kb.up"))}
			{key("↓", KEYSYM.Down, t("deskPanel.kb.down"))}
			{key("→", KEYSYM.Right, t("deskPanel.kb.right"))}
		</div>
	);
}
