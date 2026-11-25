/**
 * 原位 Rewind（回退）控制器与界面：对齐 omp 原生 Esc Esc Rewind 选择器。
 *
 * - 进入后不弹遮挡上下文的居中模态框，而是直接在真实转录流原位框选目标（默认最后一个可 rewind 对象，
 *   含用户消息、助手回复与工具调用），并在同一个玻璃输入坞托盘里显示控制条与实时过滤栏。
 * - 顶部状态胶囊悬浮在页头下方（不遮挡页头与桌面窗口控件）；底部控制条替代输入坞内容，
 *   由 ResizeObserver 自动测量高度并驱动转录底部留白。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Entry } from "../../api/client";
import { showToast } from "../../state/toasts";
import type { ToolsExpandState } from "../transcript/active-tools";
import { Icon } from "../ui/icons";
import { Button, ErrorText, Spinner } from "../ui/primitives";
import { rewindTo } from "./rewind";
import {
	buildRewindTargets,
	matchRewindTargets,
	type RewindTarget,
	type SiblingBranch,
	siblingBranchesFor,
	stepUserTurn,
} from "./rewind-model";

/** 交给 `Transcript` 的原位 Rewind 渲染态。 */
export interface RewindTranscriptState {
	/** 当前框选中的渲染行 key（`e${seq}`）。 */
	selectedKey: string | null;
	/** 所有可 rewind 的渲染行 key（支持鼠标/触屏单击直接选中）。 */
	selectableKeys: ReadonlySet<string>;
	/** 回退后将被截断（保留到分支中）的后续行 key，视觉上轻度弱化。 */
	droppedKeys: ReadonlySet<string>;
	/** 当前选中框右上角的轻量角标文案（如「编辑重发」或「保留至此」）。 */
	/** 每次批量展开/收起（Ctrl+O）递增，通知转录在重测高度后把当前框选项重新滚回视口居中。 */
	scrollNonce?: number;
	badge: string;
	/** 点击某条可 rewind 行时把框移过去。 */
	onPick: (rowKey: string) => void;
	/** 过滤中且无匹配时在转录区显示空态文案。 */
	emptyFilterText?: string;
}

interface VariantPreview {
	/** 该分叉点所有兄弟分支（0 = 当前分支，1..n = 其它分支）。 */
	branches: SiblingBranch[];
	/** 当前正在预览的分支索引（> 0 时处于兄弟分支预览态）。 */
	index: number;
}

export interface RewindController {
	active: boolean;
	/** 用于 `buildTranscript` 的预览叶子（切换到兄弟分支预览时与真实 `leafEntryId` 不同）。 */
	previewLeafId: string | undefined;
	/** 过滤态下需要保留显示的 `TranscriptItem.key` 集合；`null` 表示显示完整转录。 */
	visibleRowKeys: ReadonlySet<string> | null;
	/** 交给 `<Transcript rewind={...} />` 的原位渲染态。 */
	transcript: RewindTranscriptState | undefined;
	/** 交给 `<ToolsExpandContext.Provider />` 的全量展开态（`Ctrl+O`）。 */
	toolsExpand: ToolsExpandState;
	/** 选中目标在当前列表中的 1-based 序号与总数（过滤态为匹配列表位置）。 */
	position: { at: number; total: number };
	/** 当前选中目标。 */
	selectedTarget: RewindTarget | null;
	/** 当前点若处于分叉处，列出兄弟分支信息（`count >= 2`）。 */
	branches: { index: number; count: number; current: SiblingBranch } | null;
	/** 过滤状态。 */
	filtering: boolean;
	query: string;
	setQuery: (q: string) => void;
	openFilter: () => void;
	closeFilter: () => void;
	filterInputRef: React.RefObject<HTMLInputElement | null>;
	/** 工具与思考块全量展开切换（`Ctrl+O`）。 */
	expanded: boolean;
	toggleExpanded: () => void;
	/** 导航与提交动作。 */
	step: (delta: -1 | 1) => void;
	lateral: (delta: -1 | 1) => void;
	confirm: () => void;
	cancel: () => void;
	busy: boolean;
	error: unknown;
}

export function useRewindMode({
	threadId,
	entries,
	leafEntryId,
	active,
	startAt,
	hasEarlier,
	onLoadEarlier,
	onClose,
}: {
	threadId: string;
	entries: readonly Entry[];
	leafEntryId: string | undefined;
	active: boolean;
	startAt?: string;
	hasEarlier?: boolean;
	onLoadEarlier?: () => Promise<void>;
	onClose: () => void;
}): RewindController {
	const { t } = useTranslation();
	const [selectedAnchorId, setSelectedAnchorId] = useState<string | null>(null);
	const [variant, setVariant] = useState<VariantPreview | null>(null);
	const [filtering, setFiltering] = useState(false);
	const [query, setQuery] = useState("");
	const [toolsExpand, setToolsExpand] = useState<ToolsExpandState>({
		expanded: false,
		epoch: 0,
	});
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const filterInputRef = useRef<HTMLInputElement | null>(null);
	const initializedForRef = useRef<string | null>(null);

	// 进入 / 退出 Rewind 时重置内部状态；退出时递增 epoch 把工具卡还原回默认展开态
	useEffect(() => {
		if (!active) {
			initializedForRef.current = null;
			setSelectedAnchorId(null);
			setVariant(null);
			setFiltering(false);
			setQuery("");
			setBusy(false);
			setError(null);
			setToolsExpand((prev) =>
				prev.expanded ? { expanded: false, epoch: prev.epoch + 1 } : prev,
			);
			return;
		}
		const sessionKey = `${threadId}:${startAt ?? ""}`;
		if (initializedForRef.current === sessionKey) return;
		initializedForRef.current = sessionKey;
		const initialTargets = buildRewindTargets(entries, leafEntryId);
		if (initialTargets.length === 0) {
			showToast({ text: t("rewind.empty") });
			onClose();
			return;
		}
		const hit = startAt
			? initialTargets.find((item) => item.entryIds.includes(startAt))
			: undefined;
		// 未指定 startAt 时默认框选所有可 rewind 对象中的最后一个（与 omp 一致）
		const pick = hit ?? initialTargets[initialTargets.length - 1];
		setSelectedAnchorId(pick?.anchorEntryId ?? null);
		setVariant(null);
		setFiltering(false);
		setQuery("");
		setError(null);
	}, [active, threadId, startAt, entries, leafEntryId, onClose, t]);

	const previewLeafId =
		active && variant && variant.index > 0
			? variant.branches[variant.index]?.leafEntryId
			: leafEntryId;

	const targets = useMemo(
		() => (active ? buildRewindTargets(entries, previewLeafId) : []),
		[active, entries, previewLeafId],
	);

	const selectedIndex = useMemo(() => {
		if (targets.length === 0) return -1;
		if (selectedAnchorId) {
			const idx = targets.findIndex(
				(x) => x.anchorEntryId === selectedAnchorId,
			);
			if (idx >= 0) return idx;
		}
		return targets.length - 1;
	}, [targets, selectedAnchorId]);

	const selectedTarget =
		selectedIndex >= 0 ? (targets[selectedIndex] ?? null) : null;

	// 当前分叉点的兄弟分支（过滤态下不展示分支切换，保持线性筛选体验）
	const activeBranches = useMemo(() => {
		if (!active || filtering || !selectedTarget) return null;
		if (variant && variant.index > 0) {
			const cur = variant.branches[variant.index];
			if (cur)
				return {
					branches: variant.branches,
					index: variant.index,
					current: cur,
				};
		}
		const sibs = siblingBranchesFor(
			entries,
			selectedTarget.anchorEntryId,
			leafEntryId,
		);
		if (sibs.length < 2 || !sibs[0]) return null;
		return { branches: sibs, index: 0, current: sibs[0] };
	}, [active, filtering, selectedTarget, variant, entries, leafEntryId]);

	const trimmedQuery = filtering ? query.trim() : "";
	const matchedIndices = useMemo(
		() =>
			active && trimmedQuery ? matchRewindTargets(targets, trimmedQuery) : null,
		[active, targets, trimmedQuery],
	);

	// 过滤词变化时，如果当前选中项不再匹配，自动吸附到它上方最近的匹配项（或最后一个匹配项）
	useEffect(() => {
		if (!active || !matchedIndices) return;
		if (matchedIndices.length === 0 || matchedIndices.includes(selectedIndex))
			return;
		let fallback = matchedIndices[matchedIndices.length - 1];
		for (let i = matchedIndices.length - 1; i >= 0; i--) {
			const idx = matchedIndices[i];
			if (idx !== undefined && idx < selectedIndex) {
				fallback = idx;
				break;
			}
		}
		const target = fallback !== undefined ? targets[fallback] : undefined;
		if (target) setSelectedAnchorId(target.anchorEntryId);
	}, [active, matchedIndices, selectedIndex, targets]);

	const openFilter = useCallback(() => {
		setVariant(null);
		setFiltering(true);
		requestAnimationFrame(() => filterInputRef.current?.focus());
	}, []);

	const closeFilter = useCallback(() => {
		setFiltering(false);
		setQuery("");
	}, []);

	const toggleExpanded = useCallback(() => {
		setToolsExpand((prev) => ({
			expanded: !prev.expanded,
			epoch: prev.epoch + 1,
		}));
	}, []);

	const step = useCallback(
		(delta: -1 | 1) => {
			if (targets.length === 0) return;
			if (matchedIndices) {
				if (matchedIndices.length === 0) return;
				const next =
					delta < 0
						? [...matchedIndices].reverse().find((i) => i < selectedIndex)
						: matchedIndices.find((i) => i > selectedIndex);
				if (next !== undefined && targets[next])
					setSelectedAnchorId(targets[next].anchorEntryId);
				return;
			}
			const nextIdx = selectedIndex + delta;
			if (nextIdx < 0) {
				if (hasEarlier && onLoadEarlier) void onLoadEarlier();
				return;
			}
			if (nextIdx >= targets.length) return;
			const nextTarget = targets[nextIdx];
			if (!nextTarget) return;
			// 若处于兄弟分支预览态，向上走出该兄弟分支首条进入公共前缀时退出兄弟分支预览
			if (variant && variant.index > 0) {
				const curBranch = variant.branches[variant.index];
				const forkIdx = curBranch
					? targets.findIndex(
							(x) => x.anchorEntryId === curBranch.firstTargetId,
						)
					: -1;
				if (forkIdx >= 0 && nextIdx < forkIdx) setVariant(null);
			}
			setSelectedAnchorId(nextTarget.anchorEntryId);
		},
		[
			targets,
			matchedIndices,
			selectedIndex,
			hasEarlier,
			onLoadEarlier,
			variant,
		],
	);

	const lateral = useCallback(
		(delta: -1 | 1) => {
			if (targets.length === 0) return;
			// 过滤态下 ←/→ 在命中的用户回合之间跳转
			if (matchedIndices) {
				const userMatches = matchedIndices.filter((i) => {
					const item = targets[i];
					return item ? item.isUserTurn || item.kind === "user" : false;
				});
				const next =
					delta < 0
						? [...userMatches].reverse().find((i) => i < selectedIndex)
						: userMatches.find((i) => i > selectedIndex);
				if (next !== undefined && targets[next])
					setSelectedAnchorId(targets[next].anchorEntryId);
				return;
			}
			if (activeBranches) {
				const nextVariant = activeBranches.index + delta;
				if (nextVariant >= 0 && nextVariant < activeBranches.branches.length) {
					const nextBranch = activeBranches.branches[nextVariant];
					if (nextBranch) {
						setVariant(
							nextVariant === 0
								? null
								: { branches: activeBranches.branches, index: nextVariant },
						);
						setSelectedAnchorId(nextBranch.firstTargetId);
						return;
					}
				}
				// 已在兄弟分支末端时停住；只有在主分支（index === 0）按 ← 才继续跳到上一个用户回合
				if (activeBranches.index > 0) return;
			}
			const nextIdx = stepUserTurn(targets, selectedIndex, delta);
			if (nextIdx !== null && targets[nextIdx]) {
				setVariant(null);
				setSelectedAnchorId(targets[nextIdx].anchorEntryId);
			}
		},
		[targets, matchedIndices, selectedIndex, activeBranches],
	);

	const pickByKey = useCallback(
		(rowKey: string) => {
			const hit = targets.find((x) => x.rowKey === rowKey);
			if (!hit) return;
			if (variant && variant.index > 0) {
				const curBranch = variant.branches[variant.index];
				const forkIdx = curBranch
					? targets.findIndex(
							(x) => x.anchorEntryId === curBranch.firstTargetId,
						)
					: -1;
				const hitIdx = targets.findIndex((x) => x.rowKey === rowKey);
				if (forkIdx >= 0 && hitIdx < forkIdx) setVariant(null);
			}
			setSelectedAnchorId(hit.anchorEntryId);
		},
		[targets, variant],
	);

	const confirm = useCallback(() => {
		if (busy || !selectedTarget) return;
		// 过滤无结果时回车不误触发回退
		if (matchedIndices && !matchedIndices.includes(selectedIndex)) return;
		setBusy(true);
		setError(null);
		void rewindTo(threadId, selectedTarget.targetEntryId, {
			rewindsBefore: selectedTarget.rewindsBefore,
		})
			.then(() => {
				onClose();
			})
			.catch((e: unknown) => {
				setError(e);
			})
			.finally(() => {
				setBusy(false);
			});
	}, [busy, selectedTarget, matchedIndices, selectedIndex, threadId, onClose]);

	const cancel = useCallback(() => {
		if (busy) return;
		onClose();
	}, [busy, onClose]);

	// Rewind 模式全局键盘导航（冒泡前拦截 Esc / 方向键 / f / Ctrl+O / Enter）
	useEffect(() => {
		if (!active) return;
		const onKeyDown = (e: KeyboardEvent) => {
			if (e.isComposing) return;
			const inFilter =
				filterInputRef.current !== null &&
				document.activeElement === filterInputRef.current;

			// Ctrl+O 切换全部工具与思考块展开
			if (e.ctrlKey && !e.metaKey && !e.altKey && e.key.toLowerCase() === "o") {
				e.preventDefault();
				toggleExpanded();
				return;
			}
			if (e.key === "Escape") {
				e.preventDefault();
				e.stopPropagation();
				if (filtering) closeFilter();
				else cancel();
				return;
			}
			if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
				e.preventDefault();
				confirm();
				return;
			}
			if (e.key === "ArrowUp") {
				e.preventDefault();
				step(-1);
				return;
			}
			if (e.key === "ArrowDown") {
				e.preventDefault();
				step(1);
				return;
			}
			if (inFilter) {
				if (e.key === "Backspace" && query === "") {
					e.preventDefault();
					closeFilter();
				}
				return;
			}
			if (e.metaKey || e.ctrlKey || e.altKey) return;
			if (e.key === "ArrowLeft") {
				e.preventDefault();
				lateral(-1);
			} else if (e.key === "ArrowRight") {
				e.preventDefault();
				lateral(1);
			} else if (e.key === "k") {
				e.preventDefault();
				step(-1);
			} else if (e.key === "j") {
				e.preventDefault();
				step(1);
			} else if (e.key === "Home" && targets[0]) {
				e.preventDefault();
				setVariant(null);
				setSelectedAnchorId(targets[0].anchorEntryId);
			} else if (e.key === "End" && targets.length > 0) {
				e.preventDefault();
				const last = targets[targets.length - 1];
				if (last) setSelectedAnchorId(last.anchorEntryId);
			} else if (e.key === "f" || e.key === "/") {
				e.preventDefault();
				openFilter();
			}
		};
		window.addEventListener("keydown", onKeyDown, true);
		return () => window.removeEventListener("keydown", onKeyDown, true);
	}, [
		active,
		filtering,
		query,
		targets,
		toggleExpanded,
		closeFilter,
		cancel,
		confirm,
		step,
		lateral,
		openFilter,
	]);

	const visibleRowKeys = useMemo<ReadonlySet<string> | null>(() => {
		if (!active || !matchedIndices) return null;
		const set = new Set<string>();
		for (const idx of matchedIndices) {
			const item = targets[idx];
			if (item) set.add(item.rowKey);
		}
		return set;
	}, [active, matchedIndices, targets]);

	const transcript = useMemo<RewindTranscriptState | undefined>(() => {
		if (!active) return undefined;
		const selectableKeys = new Set<string>();
		const droppedKeys = new Set<string>();
		const activeIdx =
			matchedIndices && !matchedIndices.includes(selectedIndex)
				? -1
				: selectedIndex;
		for (let i = 0; i < targets.length; i++) {
			const item = targets[i];
			if (!item) continue;
			selectableKeys.add(item.rowKey);
			// 正常完整视图中，把选中目标之后的后续条目弱化，直观预览哪些会被回退裁掉
			if (!matchedIndices && activeIdx >= 0 && i > activeIdx)
				droppedKeys.add(item.rowKey);
		}
		const selectedKey =
			activeIdx >= 0 ? (targets[activeIdx]?.rowKey ?? null) : null;
		const badge = selectedTarget
			? selectedTarget.rewindsBefore
				? t("rewind.badgeEdit")
				: t("rewind.badgeKeep")
			: "";
		return {
			selectedKey,
			selectableKeys,
			droppedKeys,
			badge,
			onPick: pickByKey,
			emptyFilterText:
				matchedIndices && matchedIndices.length === 0
					? t("rewind.noMatches", { query: trimmedQuery })
					: undefined,
			scrollNonce: toolsExpand.epoch,
		};
	}, [
		active,
		targets,
		matchedIndices,
		selectedIndex,
		selectedTarget,
		pickByKey,
		trimmedQuery,
		toolsExpand.epoch,
		t,
	]);

	const position = useMemo(() => {
		if (matchedIndices) {
			const idx = matchedIndices.indexOf(selectedIndex);
			return { at: idx >= 0 ? idx + 1 : 0, total: matchedIndices.length };
		}
		return {
			at: selectedIndex >= 0 ? selectedIndex + 1 : 0,
			total: targets.length,
		};
	}, [matchedIndices, selectedIndex, targets.length]);

	return {
		active,
		previewLeafId,
		visibleRowKeys,
		transcript,
		toolsExpand,
		position,
		selectedTarget,
		branches: activeBranches
			? {
					index: activeBranches.index,
					count: activeBranches.branches.length,
					current: activeBranches.current,
				}
			: null,
		filtering,
		query,
		setQuery,
		openFilter,
		closeFilter,
		filterInputRef,
		expanded: toolsExpand.expanded,
		toggleExpanded,
		step,
		lateral,
		confirm,
		cancel,
		busy,
		error,
	};
}

/** 顶部悬浮状态胶囊：位于页头下方（不遮挡页头与桌面窗口控件），标示当前处于 Rewind 模式与位置。 */
export function RewindTopPill({
	ctrl,
	top,
}: {
	ctrl: RewindController;
	top: number;
}): ReactNode {
	const { t } = useTranslation();
	if (!ctrl.active) return null;
	return (
		<div
			data-testid="rewind-banner"
			className="pointer-events-none absolute inset-x-0 z-20 flex justify-center px-3"
			style={{ top }}
		>
			<div className="glass pointer-events-auto flex max-w-full items-center gap-2 rounded-full px-3 py-1 text-xs">
				<Icon
					name="undo"
					size={13}
					aria-hidden
					className="shrink-0 text-accent-ink"
				/>
				<span className="shrink-0 font-medium text-fg">
					{t("rewind.modeTitle")}
				</span>
				<span className="hidden truncate text-muted sm:inline">
					{t("rewind.modeSubtitle")}
				</span>
				<span
					data-testid="rewind-counter"
					className="shrink-0 rounded-full border border-border bg-inset px-2 py-px font-mono text-2xs text-muted tabular-nums"
				>
					{ctrl.position.total > 0
						? `${ctrl.position.at}/${ctrl.position.total}`
						: "0/0"}
				</span>
				{ctrl.branches && (
					<span
						data-testid="rewind-branch-badge"
						className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-inset px-2 py-px text-2xs text-accent-ink tabular-nums"
					>
						<Icon name="fork" size={11} aria-hidden />
						{t("rewind.branchCount", {
							index: ctrl.branches.index + 1,
							total: ctrl.branches.count,
						})}
					</span>
				)}
			</div>
		</div>
	);
}

const KBD =
	"items-center rounded border border-border bg-inset px-1.5 py-px font-mono text-2xs text-muted";

/** 底部 Rewind 控制条：落在原输入坞 `.glass-dock` 托盘内，提供过滤、导航快捷键提示、分支切换与确认/取消操作。 */
export function RewindDockBar({ ctrl }: { ctrl: RewindController }): ReactNode {
	const { t } = useTranslation();
	const canSubmit =
		!ctrl.busy &&
		ctrl.selectedTarget !== null &&
		(!ctrl.filtering || ctrl.position.at > 0);

	return (
		<section
			data-testid="rewind-bar"
			aria-label={t("rewind.modeTitle")}
			className="flex flex-col gap-2.5 px-5 py-3.5"
		>
			{ctrl.filtering && (
				<div className="flex items-center gap-2 rounded-md border border-border bg-inset px-2.5 py-1.5">
					<Icon
						name="search"
						size={13}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<input
						ref={ctrl.filterInputRef}
						type="text"
						data-testid="rewind-filter-input"
						value={ctrl.query}
						onChange={(e) => ctrl.setQuery(e.target.value)}
						placeholder={t("rewind.filterPlaceholder")}
						aria-label={t("rewind.filterLabel")}
						className="min-w-0 flex-1 bg-transparent text-fg text-sm outline-none placeholder:text-faint"
					/>
					<span className="shrink-0 font-mono text-2xs text-faint tabular-nums">
						{ctrl.position.total > 0
							? `${ctrl.position.at}/${ctrl.position.total}`
							: t("rewind.noMatchesShort")}
					</span>
					<button
						type="button"
						onClick={ctrl.closeFilter}
						aria-label={t("rewind.clearFilter")}
						className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-faint hover:bg-hover hover:text-fg"
					>
						<Icon name="close" size={12} />
					</button>
				</div>
			)}

			{ctrl.branches && (
				<div className="flex min-w-0 items-center gap-2 text-xs">
					<span className="shrink-0 font-medium text-muted">
						{t("rewind.branchesLabel")}
					</span>
					<div className="flex min-w-0 flex-1 items-center gap-1">
						<button
							type="button"
							disabled={ctrl.branches.index === 0}
							onClick={() => ctrl.lateral(-1)}
							aria-label={t("rewind.prevBranch")}
							className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm border border-border text-muted hover:bg-hover hover:text-fg disabled:opacity-40"
						>
							<Icon name="chevronLeft" size={12} />
						</button>
						<span className="min-w-0 truncate text-fg">
							{ctrl.branches.current.isCurrent
								? t("rewind.currentBranch")
								: ctrl.branches.current.label}
						</span>
						<button
							type="button"
							disabled={ctrl.branches.index >= ctrl.branches.count - 1}
							onClick={() => ctrl.lateral(1)}
							aria-label={t("rewind.nextBranch")}
							className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm border border-border text-muted hover:bg-hover hover:text-fg disabled:opacity-40"
						>
							<Icon name="chevronRight" size={12} />
						</button>
					</div>
				</div>
			)}

			<ErrorText error={ctrl.error} />

			<div className="flex flex-wrap items-center justify-between gap-2">
				{/* 左侧：窄屏提供触屏步进与功能钮，宽屏展示清晰快捷键提示 */}
				<div className="flex min-w-0 flex-wrap items-center gap-1.5 text-2xs text-faint">
					<div className="flex items-center gap-1 sm:hidden">
						<button
							type="button"
							onClick={() => ctrl.step(-1)}
							aria-label={t("rewind.stepPrev")}
							className="inline-flex h-7 w-7 items-center justify-center rounded-sm border border-border text-muted hover:bg-hover hover:text-fg"
						>
							<Icon name="chevronUp" size={13} />
						</button>
						<button
							type="button"
							onClick={() => ctrl.step(1)}
							aria-label={t("rewind.stepNext")}
							className="inline-flex h-7 w-7 items-center justify-center rounded-sm border border-border text-muted hover:bg-hover hover:text-fg"
						>
							<Icon name="chevronDown" size={13} />
						</button>
					</div>
					<span className="hidden items-center gap-1 sm:inline-flex">
						<kbd className={`inline-flex ${KBD}`}>↑↓</kbd>
						<span>{t("rewind.keyStep")}</span>
					</span>
					<span className="hidden items-center gap-1 sm:inline-flex">
						<kbd className={`inline-flex ${KBD}`}>←→</kbd>
						<span>
							{ctrl.branches
								? t("rewind.keyBranches")
								: t("rewind.keyUserTurns")}
						</span>
					</span>
					<button
						type="button"
						onClick={ctrl.filtering ? ctrl.closeFilter : ctrl.openFilter}
						className={`inline-flex h-7 items-center gap-1 rounded-sm px-2 text-2xs transition-colors duration-150 sm:h-6 sm:px-1.5 ${
							ctrl.filtering
								? "bg-hover text-fg"
								: "text-muted hover:bg-hover hover:text-fg"
						}`}
					>
						<kbd className={`hidden sm:inline-flex ${KBD}`}>f</kbd>
						<Icon name="search" size={12} className="sm:hidden" />
						<span>{t("rewind.keyFilter")}</span>
					</button>
					<button
						type="button"
						onClick={ctrl.toggleExpanded}
						aria-pressed={ctrl.expanded}
						className={`inline-flex h-7 items-center gap-1 rounded-sm px-2 text-2xs transition-colors duration-150 sm:h-6 sm:px-1.5 ${
							ctrl.expanded
								? "bg-hover text-fg"
								: "text-muted hover:bg-hover hover:text-fg"
						}`}
					>
						<kbd className={`hidden sm:inline-flex ${KBD}`}>Ctrl+O</kbd>
						<Icon
							name={ctrl.expanded ? "minimize" : "maximize"}
							size={12}
							className="sm:hidden"
						/>
						<span>
							{ctrl.expanded ? t("rewind.keyCollapse") : t("rewind.keyExpand")}
						</span>
					</button>
				</div>

				{/* 右侧：取消与主动作按钮 */}
				<div className="ml-auto flex shrink-0 items-center gap-2">
					<Button
						size="sm"
						disabled={ctrl.busy}
						onClick={ctrl.cancel}
						data-testid="rewind-cancel"
					>
						{t("common.cancel")}
					</Button>
					<Button
						size="sm"
						variant="primary"
						disabled={!canSubmit}
						onClick={ctrl.confirm}
						data-testid="rewind-submit"
					>
						{ctrl.busy ? (
							<Spinner size={12} />
						) : ctrl.selectedTarget?.rewindsBefore ? (
							t("rewind.submitEdit")
						) : (
							t("rewind.submitHere")
						)}
					</Button>
				</div>
			</div>
		</section>
	);
}
