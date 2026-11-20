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
import {
	readDomSelection,
	selectionText,
} from "../../lib/context-menu/selection";
import type {
	Anchor,
	DomSelection,
	SelectionContext,
} from "../../lib/context-menu/types";
import { normalizeQuote } from "../../lib/quote";
import { runSideAsk, useSideAskBusy } from "../../state/side-ask";
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
	/** 选区完整落在同一条 agent 消息里：可以「引用回复」 */
	quotable: boolean;
	/** 可以「解释」时的旁路提问上下文（有回答线程才给） */
	ask: SelectionContext | null;
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

type SelectionRead =
	| { kind: "none" }
	/** 有选区，但既不能引用也不能解释（不在转录里、只有空白等）；`inRoot` = 选区落在转录里 */
	| { kind: "other"; inRoot: boolean }
	| { kind: "quote"; quote: SelectionQuote };

function readSelection(
	root: HTMLElement,
	content: HTMLElement,
	insetTop: number,
	describe: (sel: DomSelection) => SelectionContext | null,
): SelectionRead {
	const sel = document.getSelection();
	if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return { kind: "none" };
	const range = sel.getRangeAt(0);
	const inRoot =
		root.contains(range.startContainer) || root.contains(range.endContainer);
	const dom = readDomSelection();
	if (!dom || !root.contains(range.startContainer))
		return { kind: "other", inRoot };
	const scope = scopeOf(range.startContainer);
	const quotable =
		scope !== null &&
		scope === scopeOf(range.endContainer) &&
		root.contains(scope);
	const described = describe(dom);
	const ask = described?.threadId ? described : null;
	if (!quotable && !ask) return { kind: "other", inRoot };
	const text =
		scope && quotable
			? selectionText(range, scope, () => sel.toString())
			: dom.text;
	if (!text.trim()) return { kind: "other", inRoot };
	const entry =
		(scope ?? dom.startEl)
			.closest("[data-entry-id]")
			?.getAttribute("data-entry-id") ?? "";
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
			quotable,
			ask,
		},
	};
}

/**
 * 跟踪转录里的选区。`onHold(true)` = 转录里有选区，暂停贴底跟随；`onHold(false)` = 选区没了，恢复。
 * `onQuote` 收到规整过的引用原文（`lib/quote.ts` 的 `normalizeQuote`）；`describe` 给出选区的旁路提问上下文，
 * 有回答线程时胶囊上多一个「解释」。
 */
export function useQuoteSelection({
	rootRef,
	contentRef,
	enabled,
	insetTop,
	onHold,
	onQuote,
	describe,
}: {
	rootRef: RefObject<HTMLElement | null>;
	contentRef: RefObject<HTMLElement | null>;
	enabled: boolean;
	insetTop: number;
	onHold: (held: boolean) => void;
	onQuote: (text: string) => void;
	describe: (sel: DomSelection) => SelectionContext | null;
}): {
	quote: SelectionQuote | null;
	take: () => void;
	dismiss: () => void;
	explain: (anchor: Anchor) => void;
} {
	const [quote, setQuote] = useState<SelectionQuote | null>(null);
	const insetRef = useRef(insetTop);
	insetRef.current = insetTop;
	const onHoldRef = useRef(onHold);
	onHoldRef.current = onHold;
	const onQuoteRef = useRef(onQuote);
	onQuoteRef.current = onQuote;
	const describeRef = useRef(describe);
	describeRef.current = describe;
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
			const read = readSelection(
				root,
				content,
				insetRef.current,
				describeRef.current,
			);
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
			if (!pointerDown) return;
			// 先复位：右键菜单开在指针下，松开落在菜单（affordance）上时也要复位，否则之后键盘扩选不再捕获
			pointerDown = false;
			if (fromAffordance(e)) return;
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
		if (!captured.current?.quotable) return;
		const text = normalizeQuote(captured.current.text);
		clear();
		document.getSelection()?.removeAllRanges();
		if (text) onQuoteRef.current(text);
	}, [clear]);

	const dismiss = useCallback(() => {
		dismissed.current = captured.current?.sig ?? null;
		setQuote(null);
	}, []);

	/** 「解释」：解释卡锚在胶囊按钮上 */
	const explain = useCallback((anchor: Anchor) => {
		const cur = captured.current;
		if (!cur?.ask) return;
		dismissed.current = cur.sig;
		setQuote(null);
		void runSideAsk({ kind: "explain" }, { ...cur.ask, anchor });
	}, []);

	return { quote, take, dismiss, explain };
}

// 绝对定位 + left-1/2 的胶囊按收缩适配取宽，可用宽度只剩容器的一半：w-max 与 nowrap 保证按钮文字不被折成两行
const PILL =
	"glass pop-in z-20 inline-flex w-max items-center whitespace-nowrap rounded-full text-fg";

function rectOf(el: Element): Anchor {
	const r = el.getBoundingClientRect();
	return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
}

/** 胶囊里的「解释」：手动问的 btw 在答时置灰。 */
function ExplainButton({
	quote,
	onExplain,
	className,
	iconSize,
}: {
	quote: SelectionQuote;
	onExplain: (anchor: Anchor) => void;
	className: string;
	iconSize: number;
}): ReactNode {
	const { t } = useTranslation();
	const busy = useSideAskBusy(quote.ask?.threadId ?? null);
	return (
		<button
			type="button"
			disabled={busy}
			title={busy ? t("sideAsk.busy") : undefined}
			onMouseDown={(e) => e.preventDefault()}
			onClick={(e) => onExplain(rectOf(e.currentTarget))}
			className={`${className} disabled:opacity-40`}
		>
			<Icon name="sparkle" size={iconSize} aria-hidden className="shrink-0" />
			{t("contextMenu.explain")}
		</button>
	);
}

const DIVIDER = (
	<span aria-hidden className="h-5 w-px bg-[var(--glass-line)]" />
);

/** 宽屏：悬在选区首行上方（或下方）的玻璃小胶囊，放在转录内容层里随正文滚动。 */
export function QuoteChip({
	quote,
	onTake,
	onExplain,
}: {
	quote: SelectionQuote;
	onTake: () => void;
	onExplain: (anchor: Anchor) => void;
}): ReactNode {
	const { t } = useTranslation();
	const item =
		"inline-flex h-8 items-center gap-1.5 rounded-full px-3 font-medium text-muted text-xs hover:text-fg";
	return (
		<div
			data-quote-affordance=""
			className={`${PILL} absolute h-8 -translate-x-1/2`}
			style={{
				left: quote.x,
				top: quote.below
					? quote.bottom + CHIP_GAP
					: quote.top - CHIP_GAP - CHIP_H,
			}}
		>
			{quote.quotable && (
				<button
					type="button"
					// 按下时不抢焦点、不清选区：点完之前选区一直高亮
					onMouseDown={(e) => e.preventDefault()}
					onClick={onTake}
					className={item}
				>
					<Icon name="quote" size={14} aria-hidden className="shrink-0" />
					{t("transcript.quoteReply")}
				</button>
			)}
			{quote.quotable && quote.ask && DIVIDER}
			{quote.ask && (
				<ExplainButton
					quote={quote}
					onExplain={onExplain}
					className={item}
					iconSize={14}
				/>
			)}
		</div>
	);
}

/** 手机 / 触屏：悬在输入坞上方的胶囊（系统选区菜单盖不到），右侧带关闭钮。 */
export function QuoteBar({
	quote,
	bottom,
	onTake,
	onExplain,
	onDismiss,
}: {
	quote: SelectionQuote;
	/** 离转录底边的距离（px）：托盘顶边上方 8px */
	bottom: number;
	onTake: () => void;
	onExplain: (anchor: Anchor) => void;
	onDismiss: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const item =
		"inline-flex h-10 items-center gap-2 rounded-full pr-3 pl-4 font-medium";
	return (
		<div
			data-quote-affordance=""
			className={`${PILL} absolute left-1/2 h-10 -translate-x-1/2 text-sm`}
			style={{ bottom }}
		>
			{quote.quotable && (
				<button
					type="button"
					onMouseDown={(e) => e.preventDefault()}
					onClick={onTake}
					className={item}
				>
					<Icon name="quote" size={16} aria-hidden className="shrink-0" />
					{t("transcript.quoteReply")}
				</button>
			)}
			{quote.quotable && quote.ask && DIVIDER}
			{quote.ask && (
				<ExplainButton
					quote={quote}
					onExplain={onExplain}
					className={item}
					iconSize={16}
				/>
			)}
			{DIVIDER}
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
