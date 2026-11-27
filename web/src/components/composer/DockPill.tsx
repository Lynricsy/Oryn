/**
 * 收起的输入坞（手机）：往上翻历史时，整块托盘收成一条 40px 高的扁胶囊，只留读数——当前状态、模型、上下文、成本、草稿。
 * 托盘里输入区之上的附属内容（提问、权限确认、计划、环境提示、排队等）一起收进来，胶囊最前面以「N 项待你处理」等示意。
 * 点胶囊、往下翻回来或回到底部时展开回原托盘（ThreadPage 决定何时收起 / 展开）。
 *
 * 变形（`useDockCollapse`）：同一块玻璃的宽、高、圆角用 Web Animations 从当前尺寸插值到目标尺寸（被打断时从动画中的实际尺寸接着走）；
 * 托盘内容在变形与收起期间按原宽度贴底绝对定位，只被收窄的玻璃裁掉、不重排；内容与胶囊交叉淡化。静态样式在 index.css 的 `.glass-dock[data-dock]`。
 */
import {
	type MouseEvent,
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { formatCost } from "../../lib/format";
import { isThreadBusy } from "../../state/actions";
import { useThread } from "../../state/stores";
import { dockPendingUi } from "../../state/thread-reducer";
import { Ring, thresholdTone } from "../ui/data";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { Spinner } from "../ui/primitives";
import { useModelName, useSessionCost } from "./ComposerMeta";

/** 收起后胶囊的高度（px），与 index.css 的 `.glass-dock[data-dock="mini"]` 一致。 */
export const PILL_H = 40;
const MORPH_MS = 340;
const EASE = "cubic-bezier(0.2, 0.8, 0.2, 1)";

/** 托盘里输入区之上的附属内容：数量与最重的色调（有 `role="alert"` 的算错误）。 */
interface Notes {
	count: number;
	err: boolean;
}

/** 数托盘内容里输入框所在那一节之前的各节（可见的才算）；节增减时重数。 */
function useDockNotes(bodyRef: RefObject<HTMLElement | null>): Notes {
	const [notes, setNotes] = useState<Notes>({ count: 0, err: false });
	useLayoutEffect(() => {
		const body = bodyRef.current;
		if (!body) return;
		const count = () => {
			let n = 0;
			let err = false;
			for (const el of Array.from(body.children)) {
				if (el.querySelector("textarea")) break;
				if (!(el instanceof HTMLElement) || el.offsetHeight === 0) continue;
				n++;
				if (el.matches("[role=alert]") || el.querySelector("[role=alert]"))
					err = true;
			}
			setNotes((prev) =>
				prev.count === n && prev.err === err ? prev : { count: n, err },
			);
		};
		count();
		if (typeof MutationObserver === "undefined") return;
		const mo = new MutationObserver(count);
		mo.observe(body, { childList: true });
		return () => mo.disconnect();
	}, [bodyRef]);
	return notes;
}

/** 收起后的胶囊内容：整条是一个按钮，点它展开输入坞。`draft` = 收起时输入框里有没写完的字。 */
export function DockPill({
	threadId,
	bodyRef,
	measureRef,
	draft,
	shown,
	onExpand,
}: {
	threadId: string;
	bodyRef: RefObject<HTMLElement | null>;
	/** 内容行（量宽度用：胶囊收起后的宽度就是这一行的自然宽度） */
	measureRef: RefObject<HTMLSpanElement | null>;
	draft: boolean;
	shown: boolean;
	onExpand: (e: MouseEvent<HTMLButtonElement>) => void;
}): ReactNode {
	const { t } = useTranslation();
	// 视图的阻塞请求（M33）在转录里作答，不算输入坞里「待你处理」的事
	const pending = useThread(threadId, (s) => dockPendingUi(s).length);
	const plan = useThread(threadId, (s) => s.planProposal !== null);
	const busy = useThread(threadId, isThreadBusy);
	const ctx = useThread(threadId, (s) => s.state?.contextUsage);
	const { model, name, short } = useModelName(threadId);
	const { cost, tps } = useSessionCost(threadId);
	const notes = useDockNotes(bodyRef);
	const pct = ctx ? Math.round(ctx.percent) : null;

	// 「要你做的事」只放最要紧的一件：待你处理 > 计划待确认 > 其它提示；主题色只给前两种
	let attention: ReactNode = null;
	if (pending > 0 || plan || notes.count > 0) {
		const yours = pending > 0 || plan;
		const text =
			pending > 0
				? t("dockPill.pending", { count: pending })
				: plan
					? t("dockPill.plan")
					: t("dockPill.notes", { count: notes.count });
		attention = (
			<span
				data-testid="dock-pill-lead"
				className={`inline-flex items-center gap-1.5 font-semibold ${yours ? "text-accent-ink" : notes.err ? "text-err" : "text-warn-ink"}`}
			>
				<span
					aria-hidden
					className={`h-2 w-2 rounded-full ${yours ? "bg-accent" : notes.err ? "bg-err" : "bg-warn"}`}
				/>
				{text}
			</span>
		);
	}
	// agent 的运行状态与「要你做的事」分开表达：运行中是中性色转圈；同时有待处理时只留转圈，把宽度让给待处理。
	// 速度是最低优先级的读数，放在读数区末尾（窄屏先被裁掉）
	const rate =
		typeof tps === "number" && tps > 0
			? t("status.tps", { n: tps.toFixed(0) })
			: null;
	const running = busy && (
		<span
			data-testid="dock-pill-running"
			className="inline-flex items-center gap-1.5 font-medium text-fg"
		>
			<Spinner size={11} />
			{attention ? (
				<span className="sr-only">{t("dockPill.working")}</span>
			) : (
				t("dockPill.working")
			)}
		</span>
	);

	return (
		<button
			type="button"
			data-testid="dock-pill"
			aria-label={t("dockPill.expand")}
			aria-expanded={false}
			inert={!shown}
			tabIndex={shown ? 0 : -1}
			onClick={onExpand}
			// 贴底：变形时胶囊行就在最终位置，外壳从上往下收拢到它身上（展开时反过来），中途不出现悬在中间的空壳
			className="dock-pill absolute inset-x-0 bottom-0 flex h-10 items-center justify-center rounded-[inherit] text-muted text-xs outline-offset-2 active:scale-[0.98]"
		>
			{/*
			 * 两端固定：「要你做的事」/ 运行中在前，展开箭头在后，永远完整。中间读数按优先级让位：草稿 > 模型（先缩成只剩 logo）
			 * > 上下文 > 成本 > 速度，放不下时从尾部被裁掉。胶囊宽度 = 内容自然宽度，最宽到容器宽（见 useDockCollapse）
			 */}
			<span
				ref={measureRef}
				className="flex h-full w-max max-w-full items-center gap-2.5 whitespace-nowrap pr-1.5 pl-3.5"
			>
				{(attention || running) && (
					<span className="inline-flex shrink-0 items-center gap-2.5">
						{attention}
						{running}
						<span aria-hidden className="h-3.5 w-px bg-[var(--glass-line)]" />
					</span>
				)}
				<span
					data-testid="dock-pill-readouts"
					className="flex min-w-0 flex-1 items-center gap-2.5 overflow-hidden"
				>
					{draft && (
						<span className="inline-flex shrink-0 items-center gap-1 text-fg">
							<Icon name="edit" size={12} aria-hidden />
							{t("dockPill.draft")}
						</span>
					)}
					<span className="inline-flex min-w-[13px] items-center gap-1.5 font-medium text-fg">
						<ModelLogo
							provider={model?.provider}
							model={model?.id}
							size={13}
							className="shrink-0"
						/>
						<span className="min-w-0 max-w-28 truncate">{short}</span>
						{short !== name && <span className="sr-only">{name}</span>}
					</span>
					{pct !== null && (
						<span className="inline-flex shrink-0 items-center gap-1 tabular-nums">
							<Ring
								value={pct}
								size={12}
								tone={thresholdTone(pct)}
								label={t("status.context", { pct })}
							/>
							{pct}%
						</span>
					)}
					<span className="shrink-0 tabular-nums">
						<span className="sr-only">{t("stat.cost")}</span>
						{formatCost(cost)}
					</span>
					{busy && rate && (
						<span className="shrink-0 text-muted tabular-nums">{rate}</span>
					)}
				</span>
				<span
					aria-hidden
					className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-[var(--glass-line)] text-muted"
				>
					<Icon name="chevronUp" size={13} weight="bold" />
				</span>
			</span>
		</button>
	);
}

/** 胶囊内容行的自然宽度：行平时被 `max-w-full` 限在胶囊里（读数区收缩、尾部被裁），量的时候临时放开。 */
function naturalWidth(row: HTMLElement): number {
	row.style.maxWidth = "none";
	const width = row.scrollWidth;
	row.style.maxWidth = "";
	return width;
}

/**
 * 托盘在展开 / 收起之间变形。`mini` 由调用方给；托盘的 `data-dock` 与它同步渲染，这里只负责过渡：
 * 量出两端的尺寸（展开 = 容器宽 × 内容高，收起 = 胶囊行宽 × PILL_H），从当前尺寸动画到目标尺寸。
 * 变形期间托盘挂 `data-morphing`（内容改为贴底绝对定位、玻璃裁剪）；结束后调 `onSettle` 让调用方重新量高度。
 */
export function useDockCollapse({
	mini,
	trayRef,
	bodyRef,
	measureRef,
	onSettle,
}: {
	mini: boolean;
	trayRef: RefObject<HTMLElement | null>;
	bodyRef: RefObject<HTMLElement | null>;
	measureRef: RefObject<HTMLElement | null>;
	onSettle: () => void;
}): void {
	const openRadius = useRef<string | null>(null);
	const prev = useRef(mini);
	const settleRef = useRef(onSettle);
	settleRef.current = onSettle;

	// 收起后胶囊内容变宽 / 变窄（待处理数、草稿、读数变化）或容器变宽窄（旋转）：重算胶囊宽度（index.css 里有 width 过渡）。
	// 内容行被限在胶囊里，变宽时自身尺寸不变，所以看的是内容变动（MutationObserver）与容器尺寸（ResizeObserver）
	useEffect(() => {
		const tray = trayRef.current;
		const row = measureRef.current;
		const container = tray?.parentElement;
		if (!tray || !row || !container) return;
		const fitPill = () => {
			const border = tray.offsetWidth - tray.clientWidth;
			tray.style.setProperty(
				"--pill-w",
				`${Math.min(naturalWidth(row) + border, container.clientWidth)}px`,
			);
		};
		fitPill();
		const mo = new MutationObserver(fitPill);
		mo.observe(row, { subtree: true, childList: true, characterData: true });
		const ro =
			typeof ResizeObserver === "undefined"
				? null
				: new ResizeObserver(fitPill);
		ro?.observe(container);
		return () => {
			mo.disconnect();
			ro?.disconnect();
		};
	}, [trayRef, measureRef]);

	useLayoutEffect(() => {
		const tray = trayRef.current;
		const body = bodyRef.current;
		const row = measureRef.current;
		if (!tray || !body || !row) return;
		if (!mini && !tray.hasAttribute("data-morphing"))
			openRadius.current = getComputedStyle(tray).borderRadius;
		if (prev.current === mini) return;
		prev.current = mini;

		const parent = tray.parentElement?.clientWidth ?? tray.offsetWidth;
		const border = tray.offsetWidth - tray.clientWidth;
		tray.style.setProperty("--open-w", `${parent - border}px`);
		const pillW = Math.min(naturalWidth(row) + border, parent);
		tray.style.setProperty("--pill-w", `${pillW}px`);
		// 内容在变形期间是绝对定位（不撑高托盘），按展开宽度量出的就是展开后的高度
		tray.setAttribute("data-morphing", "");
		const open = {
			width: `${parent}px`,
			height: `${body.offsetHeight + border}px`,
			borderRadius: openRadius.current ?? "32px",
		};
		const closed = {
			width: `${pillW}px`,
			height: `${PILL_H}px`,
			borderRadius: `${PILL_H / 2}px`,
		};
		const settle = () => {
			tray.removeAttribute("data-morphing");
			settleRef.current();
		};
		const animated = typeof tray.animate === "function";
		const running = animated
			? tray.getAnimations().find((a) => a.id === "dock-collapse")
			: undefined;
		const reduce = window.matchMedia?.(
			"(prefers-reduced-motion: reduce)",
		).matches;
		if (reduce || !animated) {
			running?.cancel();
			settle();
			return;
		}
		// 被打断（展开到一半又收起）：从动画里的实际尺寸接着走
		let from = mini ? open : closed;
		if (running) {
			const cs = getComputedStyle(tray);
			from = {
				width: cs.width,
				height: cs.height,
				borderRadius: cs.borderRadius,
			};
			running.cancel();
		}
		const to = mini ? closed : open;
		const anim = tray.animate([from, to], {
			duration: MORPH_MS,
			easing: EASE,
		});
		anim.id = "dock-collapse";
		anim.onfinish = settle;
	}, [mini, trayRef, bodyRef, measureRef]);
}
