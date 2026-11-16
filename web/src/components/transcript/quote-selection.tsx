/**
 * 引用回复的选区捕获与入口：转录里选中的文字完整落在同一条 agent 消息里（`data-quote-scope`）时，
 * 记下文字与位置的快照，显示「引用回复」。流式重渲染、虚拟列表卸载行都会让活的 Range 失效，所以只存快照。
 * - 宽屏细指针：玻璃小胶囊悬在选区首行上方（上方被页头挡住时放在选区下方），放在转录内容层里随正文滚动；
 * - 手机 / 触屏：系统的选区菜单与拖柄盖在选区附近，改成悬在输入坞上方的一条胶囊（带关闭钮）。
 * 转录里有选区期间（包括选区被重渲染冲掉、但用户没有点别处）暂停贴底跟随，免得流式输出把选区推走；
 * Esc 或关闭之后，同一段没变的选区不再弹出。
 */
import {
	type ReactNode,
	type RefObject,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { normalizeQuote } from "../../lib/quote";
import { Icon } from "../ui/icons";

export interface SelectionQuote {
	/** 选中的原文（未规整） */
	text: string;
	/** 「同一段选区」的判据：所在条目 + 原文 */
	sig: string;
	/** 选区首行顶边、末行底边（相对转录内容层，px） */
	top: number;
	bottom: number;
	/** 胶囊水平中心（相对转录内容层，已避开左右边缘） */
	x: number;
	/** 首行上方放不下（被页头挡住）：胶囊放在选区下方 */
	below: boolean;
}

/** 选区塌缩前这么久内有过用户输入（按下、按键）才算用户自己取消了选区；否则是重渲染冲掉的，快照留着。 */
const USER_INPUT_MS = 800;
/** 选区变化后等多久再捕获：键盘扩选、触屏拖柄的连续变化只取最后一次。 */
const SETTLE_MS = 160;
/** 宽屏胶囊高度与离选区的距离。 */
const CHIP_H = 32;
const CHIP_GAP = 8;
/** 胶囊中心离内容层左右边缘的最小距离（胶囊约 100px 宽，再留出行内边距）。 */
const CHIP_EDGE = 72;

const AFFORDANCE = "[data-quote-affordance]";
const SCOPE = "[data-quote-scope]";

function scopeOf(node: Node | null): Element | null {
	const el = node instanceof Element ? node : (node?.parentElement ?? null);
	return el?.closest(SCOPE) ?? null;
}

/**
 * 选中的文字。公式（KaTeX）按渲染结果取会得到 MathML 与排版字形拼成的乱码，改为取它的 TeX 源码：
 * 行内 `$…$`、独立成行 `$$…$$`（选到公式的一部分也算整条）。没有公式时就是 `Selection.toString()`。
 */
function selectedText(sel: Selection, range: Range, scope: Element): string {
	const formulas = Array.from(scope.querySelectorAll(".katex")).filter((el) =>
		range.intersectsNode(el),
	);
	if (formulas.length === 0) return sel.toString();
	const host = document.createElement("div");
	host.append(range.cloneContents());
	// 克隆里每条被选到的公式都留有自己的 .katex 外壳（部分选中时是残缺的子树），与原文档里的顺序一一对应
	const clones = Array.from(host.querySelectorAll(".katex")).filter(
		(el) => !el.parentElement?.closest(".katex"),
	);
	if (clones.length !== formulas.length) return sel.toString();
	clones.forEach((clone, i) => {
		const formula = formulas[i];
		const tex = formula?.querySelector(
			'annotation[encoding="application/x-tex"]',
		)?.textContent;
		if (!formula || !tex) return;
		const fence = formula.closest(".katex-display") ? "$$" : "$";
		clone.replaceWith(`${fence}${tex.trim()}${fence}`);
	});
	// innerText 才按块级元素换行：在屏外渲染一次再取
	host.style.cssText =
		"position:fixed;left:-99999px;top:0;width:800px;white-space:normal";
	document.body.append(host);
	// biome-ignore lint/performance/useDomNodeTextContent: 要的正是按渲染结果换行的文字（段落、列表项之间的换行）
	const text = host.innerText ?? host.textContent ?? "";
	host.remove();
	return text;
}

type SelectionRead =
	| { kind: "none" }
	/** 有选区，但不能引用（跨消息、不在 agent 消息里、只有空白）；`inRoot` = 选区落在转录里 */
	| { kind: "other"; inRoot: boolean }
	| { kind: "quote"; quote: SelectionQuote };

function readSelection(
	root: HTMLElement,
	content: HTMLElement,
	insetTop: number,
): SelectionRead {
	const sel = document.getSelection();
	if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return { kind: "none" };
	const range = sel.getRangeAt(0);
	const inRoot =
		root.contains(range.startContainer) || root.contains(range.endContainer);
	const scope = scopeOf(range.startContainer);
	if (!scope || scope !== scopeOf(range.endContainer) || !root.contains(scope))
		return { kind: "other", inRoot };
	const text = selectedText(sel, range, scope);
	if (!text.trim()) return { kind: "other", inRoot };
	const entry =
		scope.closest("[data-entry-id]")?.getAttribute("data-entry-id") ?? "";
	// 首行与末行的盒子定位；行尾可能有零宽盒子，跳过（测不到盒子时退回内容层左上角）
	const rects =
		typeof range.getClientRects === "function"
			? Array.from(range.getClientRects()).filter((r) => r.width > 0)
			: [];
	const box = content.getBoundingClientRect();
	const view = root.getBoundingClientRect();
	const first = rects[0];
	const last = rects[rects.length - 1];
	const firstTop = first?.top ?? box.top;
	const center = first ? (first.left + first.right) / 2 - box.left : 0;
	const x =
		box.width > CHIP_EDGE * 2
			? Math.min(Math.max(center, CHIP_EDGE), box.width - CHIP_EDGE)
			: box.width / 2;
	return {
		kind: "quote",
		quote: {
			text,
			sig: `${entry}\u0000${text}`,
			top: firstTop - box.top,
			bottom: (last?.bottom ?? firstTop) - box.top,
			x,
			below: firstTop - view.top < insetTop + CHIP_H + CHIP_GAP,
		},
	};
}

/**
 * 跟踪转录里的选区。`onHold(true)` = 转录里有选区，暂停贴底跟随；`onHold(false)` = 选区没了，恢复。
 * `onQuote` 收到规整过的引用原文（`lib/quote.ts` 的 `normalizeQuote`）。
 */
export function useQuoteSelection({
	rootRef,
	contentRef,
	enabled,
	insetTop,
	onHold,
	onQuote,
}: {
	rootRef: RefObject<HTMLElement | null>;
	contentRef: RefObject<HTMLElement | null>;
	enabled: boolean;
	insetTop: number;
	onHold: (held: boolean) => void;
	onQuote: (text: string) => void;
}): {
	quote: SelectionQuote | null;
	take: () => void;
	dismiss: () => void;
} {
	const [quote, setQuote] = useState<SelectionQuote | null>(null);
	const insetRef = useRef(insetTop);
	insetRef.current = insetTop;
	const onHoldRef = useRef(onHold);
	onHoldRef.current = onHold;
	const onQuoteRef = useRef(onQuote);
	onQuoteRef.current = onQuote;
	/** 最近一次捕获的选区（关掉入口后仍记着，用来判断「同一段」与是否继续暂停跟随） */
	const captured = useRef<SelectionQuote | null>(null);
	/** 用户关掉入口时那段选区的判据 */
	const dismissed = useRef<string | null>(null);
	const held = useRef(false);

	const setHeld = useCallback((next: boolean) => {
		if (held.current === next) return;
		held.current = next;
		onHoldRef.current(next);
	}, []);

	const show = useCallback((next: SelectionQuote | null) => {
		captured.current = next;
		setQuote(next && next.sig !== dismissed.current ? next : null);
	}, []);

	const clear = useCallback(() => {
		dismissed.current = null;
		show(null);
		setHeld(false);
	}, [show, setHeld]);

	useEffect(() => {
		if (!enabled) return;
		let pointerDown = false;
		let lastInput = 0;
		let timer: ReturnType<typeof setTimeout> | undefined;
		const evaluate = () => {
			const root = rootRef.current;
			const content = contentRef.current;
			if (!root || !content) return;
			const read = readSelection(root, content, insetRef.current);
			if (read.kind === "quote") {
				show(read.quote);
				setHeld(true);
			} else if (read.kind === "other") {
				dismissed.current = null;
				show(null);
				setHeld(read.inRoot);
			} else if (performance.now() - lastInput < USER_INPUT_MS) clear();
			// 不是用户取消的塌缩（重渲染、行被卸载）：快照与暂停都留着
			else setHeld(captured.current !== null);
		};
		const onSelectionChange = () => {
			// 选区一出现就暂停跟随（拖选过程中也算），捕获等选区稳定、按键松开之后
			const root = rootRef.current;
			const sel = document.getSelection();
			if (
				root &&
				sel &&
				sel.rangeCount > 0 &&
				!sel.isCollapsed &&
				root.contains(sel.getRangeAt(0).startContainer)
			)
				setHeld(true);
			clearTimeout(timer);
			if (!pointerDown) timer = setTimeout(evaluate, SETTLE_MS);
		};
		const fromAffordance = (e: Event) =>
			e.target instanceof Element && e.target.closest(AFFORDANCE) !== null;
		const onPointerDown = (e: PointerEvent) => {
			if (fromAffordance(e)) return;
			pointerDown = true;
			lastInput = performance.now();
			// 选区早已被重渲染冲掉：这一下不会再触发选区变化，直接收起、恢复跟随
			const sel = document.getSelection();
			if ((captured.current || held.current) && (!sel || sel.isCollapsed))
				clear();
		};
		const onPointerUp = (e: PointerEvent) => {
			if (!pointerDown || fromAffordance(e)) return;
			pointerDown = false;
			lastInput = performance.now();
			clearTimeout(timer);
			timer = setTimeout(evaluate, 0);
		};
		const onKeyDown = (e: KeyboardEvent) => {
			const cur = captured.current;
			if (e.key === "Escape" && cur && dismissed.current !== cur.sig) {
				dismissed.current = cur.sig;
				setQuote(null);
				return;
			}
			lastInput = performance.now();
		};
		document.addEventListener("selectionchange", onSelectionChange);
		document.addEventListener("pointerdown", onPointerDown, true);
		window.addEventListener("pointerup", onPointerUp, true);
		window.addEventListener("pointercancel", onPointerUp, true);
		document.addEventListener("keydown", onKeyDown);
		return () => {
			clearTimeout(timer);
			document.removeEventListener("selectionchange", onSelectionChange);
			document.removeEventListener("pointerdown", onPointerDown, true);
			window.removeEventListener("pointerup", onPointerUp, true);
			window.removeEventListener("pointercancel", onPointerUp, true);
			document.removeEventListener("keydown", onKeyDown);
			clear();
		};
	}, [enabled, rootRef, contentRef, show, setHeld, clear]);

	const take = useCallback(() => {
		const text = normalizeQuote(captured.current?.text ?? "");
		clear();
		document.getSelection()?.removeAllRanges();
		if (text) onQuoteRef.current(text);
	}, [clear]);

	const dismiss = useCallback(() => {
		dismissed.current = captured.current?.sig ?? null;
		setQuote(null);
	}, []);

	return { quote, take, dismiss };
}

const PILL = "glass pop-in z-20 inline-flex items-center rounded-full text-fg";

/** 宽屏：悬在选区首行上方（或下方）的玻璃小胶囊，放在转录内容层里随正文滚动。 */
export function QuoteChip({
	quote,
	onTake,
}: {
	quote: SelectionQuote;
	onTake: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<button
			type="button"
			data-quote-affordance=""
			// 按下时不抢焦点、不清选区：点完之前选区一直高亮
			onMouseDown={(e) => e.preventDefault()}
			onClick={onTake}
			className={`${PILL} absolute h-8 -translate-x-1/2 gap-1.5 px-3 font-medium text-muted text-xs hover:text-fg`}
			style={{
				left: quote.x,
				top: quote.below
					? quote.bottom + CHIP_GAP
					: quote.top - CHIP_GAP - CHIP_H,
			}}
		>
			<Icon name="quote" size={14} aria-hidden className="shrink-0" />
			{t("transcript.quoteReply")}
		</button>
	);
}

/** 手机 / 触屏：悬在输入坞上方的胶囊（系统选区菜单盖不到），右侧带关闭钮。 */
export function QuoteBar({
	bottom,
	onTake,
	onDismiss,
}: {
	/** 离转录底边的距离（px）：托盘顶边上方 8px */
	bottom: number;
	onTake: () => void;
	onDismiss: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			data-quote-affordance=""
			className={`${PILL} absolute left-1/2 h-10 -translate-x-1/2 text-sm`}
			style={{ bottom }}
		>
			<button
				type="button"
				onMouseDown={(e) => e.preventDefault()}
				onClick={onTake}
				className="inline-flex h-10 items-center gap-2 rounded-full pr-3 pl-4 font-medium"
			>
				<Icon name="quote" size={16} aria-hidden className="shrink-0" />
				{t("transcript.quoteReply")}
			</button>
			<span aria-hidden className="h-5 w-px bg-[var(--glass-line)]" />
			<button
				type="button"
				aria-label={t("transcript.quoteDismiss")}
				title={t("transcript.quoteDismiss")}
				onMouseDown={(e) => e.preventDefault()}
				onClick={onDismiss}
				className="inline-grid h-10 w-10 place-items-center rounded-full text-faint hover:text-fg"
			>
				<Icon name="close" size={14} />
			</button>
		</div>
	);
}
