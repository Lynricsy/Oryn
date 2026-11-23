/**
 * 虚拟滚动转录（@tanstack/react-virtual，动态测量高度）。
 * 贴底跟随：用户停在底部时随新内容自动滚动；上翻后停止跟随并显示「回到底部」。
 * 页头与输入坞以玻璃盖在转录上方：`insetTop` / `insetBottom` 走 virtualizer 的
 * `paddingStart` / `paddingEnd`（不给滚动容器加 CSS padding，否则会破坏贴底判断），
 * 同时写成滚动容器的 scroll-padding，让键盘聚焦与 scrollIntoView 落在两块覆盖层之间。
 * 左缘只有一条锚线：agent 头像（回合起点内容上方单独一行）与其余一切左对齐的块共用；用户气泡靠右。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import {
	memo,
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	useAssistantIdentity,
	useIdentity,
	useRoleIdentities,
} from "../../api/identity-queries";
import { createAssistantConversation } from "../../api/space-queries";
import {
	resolveAskThread,
	useContextTarget,
} from "../../lib/context-menu/registry";
import { anchorOf } from "../../lib/context-menu/selection";
import { BUILTIN_ROLE_NAMES, type BuiltinRole } from "../../lib/model-roles";
import { describeOrynHeader, parseOrynMessage } from "../../lib/oryn-message";
import {
	type ModelSourceRecord,
	type RoleFace,
	resolveTurnRole,
	type SourceTrail,
	siteRoleFaces,
} from "../../lib/turn-role";
import { useBreakpoint, useCoarsePointer } from "../../lib/viewport";
import { patchComposerDraft } from "../../state/composer-drafts";
import type { JsonRecord } from "../../state/message-builder";
import { getThreadStore, useIndex, useThread } from "../../state/stores";
import type { TranscriptItem } from "../../state/transcript";
import { Badge } from "../ui/data";
import { IdentityAvatar } from "../ui/IdentityAvatar";
import { Icon, type IconName } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { describeError } from "../ui/primitives";
import { CornerLevel } from "../ui/ThinkingDial";
import {
	ActiveToolCallsContext,
	activeToolCallIds,
	NO_ACTIVE_TOOL_CALLS,
} from "./active-tools";
import { type TurnIdentities, useTranscriptContext } from "./context";
import {
	assistantEdges,
	BranchSummaryCard,
	CompactionCard,
	type FlowKind,
	flowGap,
	GAP_FLOW,
	GAP_PROC,
	MemoMessageView,
	summarizeTurn,
	ToolCard,
	type TurnUsage,
	textOfContent,
	USER_BUBBLE_TEXT,
	UserBubble,
} from "./MessageView";
import { foldMainline, type MainlineFold, type MainlineRow } from "./mainline";
import { describeModeChange } from "./mode-change";
import { type KnownModel, modelLabel } from "./model-label";
import { NoticeLine } from "./NoticeLine";
import {
	removePendingPrompts,
	usePendingPrompts,
	waitingPendingPrompts,
} from "./pending-prompt";
import { QuoteBar, QuoteChip, useQuoteSelection } from "./quote-selection";
import { findRevealHit, registerRevealer } from "./reveal";
import { transcriptSelection } from "./selection-context";

/** 滚轮 / 键盘 / 触摸之后多久内的滚动算作用户在翻动（含平滑滚动的余程）。 */
const INTENT_MS = 600;
/** 往上翻的按键（Shift+空格另判）。 */
const UP_KEYS: Record<string, true> = {
	ArrowUp: true,
	PageUp: true,
	Home: true,
};
/** 往下翻的按键（不带 Shift 的空格另判）。 */
const DOWN_KEYS: Record<string, true> = {
	ArrowDown: true,
	PageDown: true,
	End: true,
};
/** 用户朝同一方向连续翻过多少像素才报告阅读方向（`onReadDirection`），过滤手指的细小抖动。 */
const READ_TRAVEL = 24;
/** 顶部「加载更早的消息」按钮占的高度（px）。 */
const EARLIER_H = 44;
/** 定位条目后描边的时长与寻找工具卡的最多帧数。 */
const REVEAL_MS = 2400;
const REVEAL_TRIES = 30;
/** 定位描边：警示色细线，不改变布局（outline 不占位）。 */
const REVEAL_RING = [
	"outline-2",
	"outline-offset-4",
	"outline-warn",
	"rounded-md",
];

const META_ICON: Record<string, IconName> = {
	mode_change: "list",
	title_change: "edit",
	ttsr_injection: "bolt",
};

function MetaLine({
	entry,
	prevMode,
}: {
	entry: JsonRecord;
	prevMode?: JsonRecord;
}) {
	const { t } = useTranslation();
	let text = "";
	switch (entry.type) {
		case "mode_change": {
			const d = describeModeChange(entry, prevMode);
			text = t(d.key, d.params ?? {});
			break;
		}
		case "ttsr_injection":
			text = t("transcript.meta.ttsr", {
				rules: Array.isArray(entry.injectedRules)
					? entry.injectedRules.join(", ")
					: "",
			});
			break;
		case "title_change":
			text = t("transcript.meta.title", { title: String(entry.title ?? "") });
			break;
		case "reset_boundary":
			text = t("transcript.contextCleared");
			break;
		default:
			text = t("transcript.unknownEntry", { type: String(entry.type ?? "") });
	}
	const icon = META_ICON[String(entry.type)] ?? "info";
	return (
		<div className="flex min-w-0 items-center gap-2 py-0.5 text-2xs text-faint">
			<span
				aria-hidden
				className="inline-grid h-[18px] w-[18px] shrink-0 place-items-center rounded bg-inset text-muted"
			>
				<Icon name={icon} size={11} />
			</span>
			<span className="min-w-0 truncate">{text}</span>
		</div>
	);
}

function ItemView({
	item,
	turn,
	turnStart,
}: {
	item: MainlineRow;
	turn?: TurnUsage;
	turnStart: boolean;
}): ReactNode {
	const { t } = useTranslation();
	switch (item.kind) {
		case "fold":
			return <FoldRow fold={item} />;
		case "session_divider":
			return (
				<div className="flex items-center gap-3 py-2">
					<div className="h-px flex-1 bg-border" />
					<Badge icon="chats">{t("transcript.newSession")}</Badge>
					<div className="h-px flex-1 bg-border" />
				</div>
			);
		case "message":
			return (
				<MemoMessageView
					message={item.message}
					entryId={item.entryId}
					live={item.live}
					turn={turn}
					turnStart={turnStart}
				/>
			);
		case "tool":
			return <ToolCard toolCallId={item.toolCallId} name={item.toolName} />;
		case "compaction":
			return <CompactionCard entry={item.entry} />;
		case "branch_summary":
			return <BranchSummaryCard entry={item.entry} />;
		case "custom_message":
			return (
				<MemoMessageView
					message={{ ...item.entry, role: "custom" }}
					live={false}
				/>
			);
		case "meta":
			return <MetaLine entry={item.entry} prevMode={item.prevMode} />;
		case "notice":
			return <NoticeLine notice={item.notice} />;
	}
}

/**
 * 主线折叠行（M19）：一行文案（幕后来料 / 沉默的理由）+ 展开箭头；展开后原样渲染被收起的各行。
 */
function FoldRow({ fold }: { fold: MainlineFold }): ReactNode {
	const { t, i18n } = useTranslation();
	const [open, setOpen] = useState(false);
	const msg = parseOrynMessage(fold.source);
	const label =
		fold.fold === "silent"
			? fold.reason
				? t("mainline.silentReason", { reason: fold.reason })
				: t("mainline.silent")
			: t("mainline.behind", {
					source: !msg
						? ""
						: msg.header.kind === "research"
							? t("mainline.sourceResearch")
							: describeOrynHeader(msg, t, i18n.language),
				});
	return (
		<div data-testid="mainline-fold" data-fold={fold.fold}>
			<button
				type="button"
				className="group/fold flex h-7 w-full min-w-0 items-center gap-2 rounded-md text-left text-[12.5px] text-muted hover:text-fg"
				onClick={() => setOpen((v) => !v)}
				aria-expanded={open}
			>
				<Icon
					name={fold.fold === "silent" ? "minusCircle" : "bell"}
					size={14}
					className="shrink-0 text-faint"
				/>
				<span className="min-w-0 flex-1 truncate">{label}</span>
				<Icon
					name="chevronRight"
					size={12}
					className={`shrink-0 text-ghost transition-transform duration-150 group-hover/fold:text-faint ${open ? "rotate-90" : ""}`}
				/>
			</button>
			{open && (
				<div className="mt-1.5 flex flex-col gap-3 border-border border-l-2 pl-3">
					{fold.items
						.filter((it) => !isSwitchRow(it))
						.map((it) => (
							<div key={it.key} className={isUserRow(it) ? "pr-10" : undefined}>
								<ItemView item={it} turnStart={isUserRow(it)} />
							</div>
						))}
				</div>
			)}
		</div>
	);
}

/** 回合里最后一段助手正文（主线「在新对话中继续」带出的引用）。 */
function turnFinalText(turn: TurnUsage): string {
	for (let i = turn.messages.length - 1; i >= 0; i--) {
		const text = textOfContent(turn.messages[i]?.content).trim();
		if (text) return text;
	}
	return "";
}

/** 主线助手回合下的「在新对话中继续」：新建助手对话，把这段话作为引用带进它的输入区，再跳过去。 */
function ContinueInNew({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	return (
		<div className="mt-1 flex min-w-0 items-center gap-2">
			<button
				type="button"
				disabled={busy}
				className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-muted text-xs hover:bg-hover hover:text-fg disabled:opacity-60"
				onClick={() => {
					setBusy(true);
					setError(null);
					createAssistantConversation().then(
						(th) => {
							patchComposerDraft(th.id, { quote: { text } });
							navigate(`/assistant/${th.id}`);
						},
						(e: unknown) => {
							setBusy(false);
							setError(e);
						},
					);
				}}
			>
				<Icon name="chats" size={13} aria-hidden className="text-faint" />
				{t("mainline.continue")}
			</button>
			{error !== null && (
				<span role="alert" className="min-w-0 truncate text-err text-xs">
					{describeError(error, t)}
				</span>
			)}
		</div>
	);
}

/** 属于 agent 回合的行：助手消息与独立的运行中工具卡。 */
function isAgentRow(item: MainlineRow | undefined): boolean {
	if (!item) return false;
	if (item.kind === "tool") return true;
	return item.kind === "message" && item.message.role === "assistant";
}

function isUserRow(item: MainlineRow): boolean {
	return item.kind === "message" && item.message.role === "user";
}

/** 模型 / 思考级别切换行：回合头的模型胶囊与思考角标、输入坞已经显示了它们，不单独成行（主线折叠展开时同样）。 */
function isSwitchRow(item: MainlineRow): boolean {
	return (
		item.kind === "meta" &&
		(item.entry.type === "model_change" ||
			item.entry.type === "thinking_level_change")
	);
}

/** 回合的模型（第一条带模型字段的助手消息），用于头像行的身份文字。 */
export interface TurnModel {
	model: string;
	provider: string;
	/** 该回合开始时生效的思考级别（此前最近一条 thinking_level_change）；前面没有这类条目时省略 */
	thinking?: string;
}

/**
 * 已结束回合的用量汇总，以回合最后一条助手消息对象为键。历史条目的 message 对象在流式增量之间引用不变
 * （buildTranscript 只重建 item 外壳），命中缓存即复用同一个 TurnUsage，历史行的 memo 不被打破；
 * 回合内消息列表有任何一项换了对象（重新拉取、尾回合刚结束）才重算。
 */
const turnUsageCache = new WeakMap<JsonRecord, TurnUsage>();

function cachedTurnUsage(messages: readonly JsonRecord[]): TurnUsage {
	const tail = messages[messages.length - 1] as JsonRecord;
	const hit = turnUsageCache.get(tail);
	if (
		hit &&
		hit.messages.length === messages.length &&
		hit.messages.every((m, i) => m === messages[i])
	)
		return hit;
	const usage = summarizeTurn(messages);
	turnUsageCache.set(tail, usage);
	return usage;
}

/**
 * 按回合（以用户消息 / 会话分隔为界）切分：每个回合的第一条 agent 行在内容上方单独放一行头像
 * （`models` 给出该回合的模型，作为头像旁的身份文字；`trails` 是同一条消息挂的模型来源轨迹，回合头据此定 role）；
 * 回合最后一行是已结束的助手消息时，在它下面挂整回合的累计用量脚注（回合仍在进行时不挂）。
 * 连续的用户消息只有第一条放用户头像（`userStarts`）。
 * `openStart` 是最后一个助手回合的起点，且其后再没有用户消息 / 会话分隔（线程工作中时就是正在进行的回合）。
 * `hidden` 是模型 / 思考级别切换行：回合头的模型胶囊与思考角标、输入坞已经显示了它们，单独成行只是噪音；
 * 行照常参与上面的推导（各回合的思考级别取自其中的 thinking_level_change），只是不渲染。
 * 流式时每个增量都会重跑：已结束回合的汇总走 `cachedTurnUsage`，引用保持不变。
 */
export function layoutTurns(items: readonly MainlineRow[]): {
	starts: Set<number>;
	userStarts: Set<number>;
	footers: Map<number, TurnUsage>;
	models: Map<number, TurnModel>;
	trails: Map<number, SourceTrail>;
	openStart: number | undefined;
	/** 行里出现过 thinking_level_change：没出现过时各回合的级别就是会话当前级别 */
	thinkingSeen: boolean;
	/** 不渲染的行（模型 / 思考级别切换），照常参与上面的推导 */
	hidden: Set<number>;
} {
	const starts = new Set<number>();
	const userStarts = new Set<number>();
	const footers = new Map<number, TurnUsage>();
	const models = new Map<number, TurnModel>();
	const trails = new Map<number, SourceTrail>();
	let messages: JsonRecord[] = [];
	let last = -1;
	let start = -1;
	let openStart: number | undefined;
	const close = () => {
		const tail = items[last];
		if (tail?.kind === "message" && !tail.live && messages.length > 0)
			footers.set(last, cachedTurnUsage(messages));
		messages = [];
		last = -1;
	};
	let level: string | undefined;
	let thinkingSeen = false;
	const hidden = new Set<number>();
	items.forEach((it, i) => {
		if (isSwitchRow(it)) hidden.add(i);
		if (it.kind === "meta" && it.entry.type === "thinking_level_change") {
			// thinkingLevel 是实际生效的具体级别；auto 模式下它缺省时退回 configured
			const v = it.entry.thinkingLevel ?? it.entry.configured;
			level = typeof v === "string" && v ? v : undefined;
			thinkingSeen = true;
		}
		if (isUserRow(it)) {
			const prev = items[i - 1];
			if (prev === undefined || !isUserRow(prev)) userStarts.add(i);
			close();
			openStart = undefined;
			return;
		}
		// 主线的折叠行与会话分隔一样是回合边界：其后的助手正文另起一个回合（带头像）
		if (it.kind === "session_divider" || it.kind === "fold") {
			close();
			openStart = undefined;
			return;
		}
		if (!isAgentRow(it)) return;
		if (last < 0) {
			starts.add(i);
			openStart = i;
			start = i;
		}
		last = i;
		if (it.kind !== "message") return;
		messages.push(it.message);
		const { model, provider } = it.message;
		if (!models.has(start) && typeof model === "string" && model) {
			const prov = typeof provider === "string" ? provider : "";
			models.set(start, {
				model,
				provider: prov,
				...(level ? { thinking: level } : {}),
			});
			if (it.trail) trails.set(start, it.trail);
		}
	});
	close();
	return {
		starts,
		userStarts,
		footers,
		models,
		trails,
		openStart,
		thinkingSeen,
		hidden,
	};
}

/** 回合边界（用户气泡、会话分隔、提示行等非 agent 行）上下的间距。 */
const GAP_TURN = 16;

/** 回合头像行与回合第一块内容之间的距离：头像是这一回合的题头，贴着内容用近档。 */
const AVATAR_GAP = GAP_PROC;

/** `insetBottom` 里输入坞上方的留白（ThreadPage 传 `dockH + 16`）；据此找到托盘顶边。 */
const DOCK_GAP = 16;

/** 行首 / 行尾的节奏类别；`turn` 是不属于 agent 过程流的行。 */
type Edge = FlowKind | "turn";

function rowEdges(item: MainlineRow, hasFooter: boolean): [Edge, Edge] {
	if (item.kind === "tool") return ["proc", "proc"];
	if (item.kind === "message" && item.message.role === "assistant")
		return assistantEdges(item.message, item.live, hasFooter);
	return ["turn", "turn"];
}

/**
 * 每行的上边距：由上一行行尾与本行行首的类别决定（见 MessageView 的 `flowGap`），
 * 与消息内块之间的间距同一规则，虚拟行之间不再叠加各自的内边距。
 * 回合起点行以头像行开头，它的上一行必然是回合边界（用户气泡、分隔线、提示行）或列表开头，
 * 所以按 `GAP_TURN` / `GAP_FLOW` 取值，与行首块的类别无关。
 * `hidden` 里的行不渲染：上边距为 0，也不改变下一行看到的「上一行」。
 */
export function rowGaps(
	items: readonly MainlineRow[],
	footers: ReadonlyMap<number, TurnUsage>,
	hidden: ReadonlySet<number>,
): number[] {
	let prev: Edge | null = null;
	return items.map((it, i) => {
		if (hidden.has(i)) return 0;
		const [first, last] = rowEdges(it, footers.has(i));
		const gap =
			prev === null
				? GAP_FLOW
				: prev === "turn" || first === "turn"
					? GAP_TURN
					: flowGap(prev, first);
		prev = last;
		return gap;
	});
}

/** 已发出、尚未作为 user 条目出现的消息（客户端暂存，对应的 user 行到达即清除）。 */
function PendingPrompt({ text }: { text: string }) {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col items-end gap-1 pr-10 pb-3">
			<span className="sr-only">{t("transcript.you")}</span>
			<UserBubble avatar dim>
				<div className={USER_BUBBLE_TEXT}>{text}</div>
			</UserBubble>
			<span className="inline-flex items-center gap-1 text-2xs text-faint">
				<Icon name="clock" size={11} aria-hidden />
				{t("transcript.sending")}
			</span>
		</div>
	);
}

const NO_FACES: readonly RoleFace[] = [];
/** 只读且没给身份（旧分享页、其它只读视图）：一律默认品牌标，不读任何设置。 */
const DEFAULT_IDENTITIES: TurnIdentities = {
	faces: NO_FACES,
	fallbackKind: "default",
};

/**
 * 回合头的身份来源：上下文给了就用（分享页）；只读且没给 → 默认；否则读站内设置。只读时一个设置请求都不发。
 * - 助手对话（R4）：身份表为空，每个回合头都是全局助手（名字、头像留空时为「全局助手」与 sparkle）；
 * - 普通对话（R5）：设了头像的 role、没设头像的内置 role（默认字形），回落为默认 Agent 身份。
 */
function useTurnIdentities(): TurnIdentities {
	const { t } = useTranslation();
	const { threadId, readOnly, identities } = useTranscriptContext();
	const own = !identities && !readOnly;
	const assistantKind =
		useIndex((s) => s.threads[threadId]?.kind) === "assistant";
	const roles = useRoleIdentities(own && !assistantKind).data?.roles;
	const assistant = useAssistantIdentity(own && assistantKind);
	const agent = useIdentity("agent", own && !assistantKind).data;
	const assistantName = t("tabBar.assistant");
	const mine = useMemo<TurnIdentities>(() => {
		if (assistantKind)
			return {
				faces: NO_FACES,
				fallback: {
					name: assistant.name || assistantName,
					avatar: assistant.avatar,
				},
				fallbackKind: "assistant",
			};
		return {
			faces: siteRoleFaces(roles),
			fallback: {
				name: agent?.name?.trim() || undefined,
				avatar: {
					avatarIcon: agent?.avatarIcon,
					avatarImage: agent?.avatarImage,
				},
			},
			fallbackKind: "agent",
		};
	}, [roles, assistantKind, assistant, assistantName, agent]);
	return identities ?? (readOnly ? DEFAULT_IDENTITIES : mine);
}

/**
 * 回合头像行：身份头像 → 名字（有才显示）→ 模型品牌 logo（与输入坞模型胶囊同款）→ 该回合模型的短名（淡色，悬停给完整 id）；
 * 消息里没有模型信息时只放头像（与名字）。身份 = 会话来源记录判定出的 role（`face`），否则回落身份（见 `lib/turn-role.ts`）。
 */
function TurnHead({
	working,
	model,
	thinking,
	known,
	face,
	identities,
	source,
}: {
	working: boolean;
	model?: TurnModel;
	/** 该回合的思考级别；不知道时省略（不画角标） */
	thinking?: string;
	known?: KnownModel;
	face?: RoleFace;
	identities: TurnIdentities;
	/** 生效来源记录的 `source`；没有记录时不出现在 DOM 上。 */
	source?: ModelSourceRecord["source"];
}): ReactNode {
	const { t } = useTranslation();
	// 助手消息的 model 是原始模型 id（可能自带 `qwen/` 这类命名空间），provider 单独给出
	const label = model
		? modelLabel({ id: model.model, provider: model.provider }, known)
		: null;
	const kind = face ? "role" : identities.fallbackKind;
	const name = face
		? face.name ||
			(Object.hasOwn(BUILTIN_ROLE_NAMES, face.role)
				? t(`roles.names.${face.role as BuiltinRole}`)
				: face.role)
		: identities.fallback?.name;
	return (
		<div
			className="flex h-7 min-w-0 items-center gap-2"
			style={{ marginBottom: AVATAR_GAP }}
			data-turn-identity={kind}
			data-role={face?.role}
			data-turn-source={source}
		>
			<IdentityAvatar
				identity={face ?? identities.fallback?.avatar}
				of={face ? "role" : kind === "assistant" ? "assistant" : "agent"}
				size={28}
				working={working}
				glyph={face?.glyph}
			/>
			{name && (
				<span
					data-testid="turn-name"
					className="max-w-[40%] shrink-0 truncate font-medium text-fg text-sm"
				>
					{name}
				</span>
			)}
			{model && label && (
				// 模型放在胶囊里（同首页的模型胶囊），思考级别画成胶囊右下角的角标
				<span
					data-testid="turn-model"
					className="relative flex h-6 min-w-0 items-center gap-1.5 rounded-full border border-border bg-panel/70 px-2.5 font-medium text-muted text-xs"
					title={label.full}
				>
					<ModelLogo
						model={model.model}
						provider={model.provider || undefined}
						size={14}
						className="shrink-0"
					/>
					<span className="min-w-0 truncate">{label.short}</span>
					{thinking && <CornerLevel level={thinking} />}
				</span>
			)}
		</div>
	);
}

/**
 * memo：线程页因页头 / 面板 / 侧栏开合等与转录无关的状态重渲染时（每次 260ms 开合的第一帧），不必重算整段虚拟列表；
 * 条目变化时 `items` 换引用照常更新。调用方须让 `footer`、回调保持稳定（useMemo / useCallback）。
 */
export const Transcript = memo(function Transcript({
	items,
	footer,
	insetTop = 0,
	insetBottom = 0,
	startAtTop = false,
	onReadDirection,
	trayTop: trayTopOverride,
	hasEarlier = false,
	onLoadEarlier,
	mainline = false,
}: {
	items: TranscriptItem[];
	footer?: ReactNode;
	/** 顶部被玻璃页头覆盖的高度（px）。 */
	insetTop?: number;
	/** 底部被玻璃输入坞覆盖的高度（px）：输入坞高度 + `DOCK_GAP` 留白（ThreadPage 传入 `dockH + 16`）。 */
	insetBottom?: number;
	/** 从开头读起（快照分享页）：不贴底，内容增长时不跟随，直到读者自己滚到底。 */
	startAtTop?: boolean;
	/**
	 * 用户的阅读方向：`back` = 离开底部往上翻历史（同向连续翻过 READ_TRAVEL），`forward` = 往下翻回来或回到底部。
	 * 只认用户的滚动（滚轮 / 触摸 / 按键 / 拖滚动条），测高补偿与跟随新内容的程序滚动不报告；回到底部除外。
	 */
	onReadDirection?: (dir: "back" | "forward") => void;
	/** 输入坞托盘顶边离底部的距离（px），「回到底部」悬在它上方；默认按 `insetBottom` 推算。输入坞收成胶囊时托盘比留白矮，由调用方给出。 */
	trayTop?: number;
	/** 还有更早的条目未加载（M19 尾部订阅）：顶部显示「加载更早的消息」。 */
	hasEarlier?: boolean;
	/** 加载更早的一页；条目并入后保持阅读位置不动。 */
	onLoadEarlier?: () => Promise<void>;
	/** 主线对话（M19）：自动来料与幕后过程、沉默的回合各折叠成一行，助手回合下有「在新对话中继续」。 */
	mainline?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const { threadId, readOnly } = useTranscriptContext();
	const pending = usePendingPrompts(threadId);
	// 对应的 user 消息到了的不再画，避免同一句话出现两次
	const pendingPrompts = useMemo(
		() =>
			pending.length === 0
				? pending
				: waitingPendingPrompts(
						pending,
						items.filter(isUserRow).map((it) => it.seq),
					),
		[pending, items],
	);
	// 全部到了才一并丢掉暂存（逐条丢会让后面的气泡再认领一次前面的 user 行）
	useEffect(() => {
		if (pending.length > 0 && pendingPrompts.length === 0)
			removePendingPrompts(
				threadId,
				pending.map((p) => p.id),
			);
	}, [pending, pendingPrompts, threadId]);
	// 显示行：主线把自动来料与沉默的回合折叠起来，其余转录原样
	const rows = useMemo<MainlineRow[]>(
		() => (mainline ? foldMainline(items) : items),
		[mainline, items],
	);
	const turns = useMemo(() => layoutTurns(rows), [rows]);
	const identities = useTurnIdentities();
	// 线程工作中时，只有正在进行的那个助手回合的头像闪（历史回合一律静止）。
	// 状态以线程索引为准；不在索引里的线程（分享页）取线程 store 自己的摘要
	const indexState = useIndex((s) => s.threads[threadId]?.state);
	const ownState = useThread(threadId, (s) => s.thread?.state);
	const working = (indexState ?? ownState) === "working";
	const liveStart = working ? turns.openStart : undefined;
	// 当前一批工具调用默认展开：模型开始下一批调用或回合结束（不再 working）时收起
	const activeTools = useMemo(
		() => (working ? activeToolCallIds(rows) : NO_ACTIVE_TOOL_CALLS),
		[working, rows],
	);
	const sessionModel = useThread(threadId, (s) => s.state?.model);
	const sessionThinking = useThread(threadId, (s) => s.state?.thinkingLevel);
	const gaps = useMemo(
		() => rowGaps(rows, turns.footers, turns.hidden),
		[rows, turns],
	);
	const scrollRef = useRef<HTMLDivElement>(null);
	const [atBottom, setAtBottom] = useState(true);
	const stick = useRef(!startAtTop);
	// 用户向上阅读的意图：滚轮 / 键盘 / 触摸之后 INTENT_MS 内，或按住滚动条往上拖。
	// 只有它能让转录退出跟随；测高补偿、程序写 scrollTop 等非用户滚动不改变跟随状态
	const intentUntil = useRef(0);
	// 往下翻的意图（只用于报告阅读方向，不影响跟随）
	const forwardUntil = useRef(0);
	const dragging = useRef(false);
	const touchY = useRef<number | null>(null);
	const lastTop = useRef(0);
	// 同一方向连续翻过的距离（换方向清零），超过 READ_TRAVEL 报告一次
	const travel = useRef({ dir: 0, px: 0 });
	const onReadRef = useRef(onReadDirection);
	onReadRef.current = onReadDirection;
	const count = rows.length + 1; // 末尾一项放 footer（工作中指示等）
	const virtualizer = useVirtualizer({
		count,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => 96,
		overscan: 6,
		paddingStart: insetTop + (hasEarlier ? EARLIER_H : 0),
		paddingEnd: insetBottom,
		getItemKey: (i) => (i < rows.length ? (rows[i]?.key ?? i) : "__footer__"),
	});
	const total = virtualizer.getTotalSize();

	const scrollToEnd = useCallback(() => {
		const el = scrollRef.current;
		if (el) el.scrollTop = el.scrollHeight;
	}, []);

	// 引用回复：转录里有选区时暂停贴底跟随（stick 不变），选区没了再补一次贴底
	const hold = useRef(false);
	const onHold = useCallback(
		(held: boolean) => {
			hold.current = held;
			if (!held && stick.current) scrollToEnd();
		},
		[scrollToEnd],
	);
	const contentRef = useRef<HTMLDivElement>(null);
	const selection = useQuoteSelection({
		rootRef: scrollRef,
		contentRef,
		enabled: !readOnly,
		insetTop,
		onHold,
		onQuote: (text) => patchComposerDraft(threadId, { quote: { text } }),
		// 不传 quote：胶囊的引用仍走上面的 onQuote
		describe: (dom) =>
			transcriptSelection(dom, {
				storeThreadId: threadId,
				askThreadId: resolveAskThread(scrollRef.current),
				fallback: anchorOf(dom, 0, 0),
			}),
	});
	// 右键 / 菜单键：转录里的选区交给旁路提问（AI 动作归哪个线程由外层页面登记）
	const ctxRef = useContextTarget<HTMLDivElement>({
		selection: (sel, req) =>
			transcriptSelection(sel, {
				storeThreadId: threadId,
				askThreadId: req.askThreadId,
				quote: readOnly
					? undefined
					: (text) => patchComposerDraft(threadId, { quote: { text } }),
				fallback: anchorOf(null, req.x, req.y),
			}),
	});
	// 合并 ref 要稳定：内联回调每次渲染都会先置 null 再挂上，子组件的 layout effect 会读到空的滚动容器
	const setScrollEl = useCallback(
		(el: HTMLDivElement | null) => {
			scrollRef.current = el;
			ctxRef(el);
		},
		[ctxRef],
	);
	// 手机与触屏：系统选区菜单盖在选区附近，入口改放在输入坞上方
	const bp = useBreakpoint();
	const coarse = useCoarsePointer();
	const quoteBar = selection.quote !== null && (bp === "compact" || coarse);

	const onScroll = useCallback(() => {
		const el = scrollRef.current;
		if (!el) return;
		const top = el.scrollTop;
		const delta = top - lastTop.current;
		const movedUp = delta < -1;
		lastTop.current = top;
		const bottom = el.scrollHeight - top - el.clientHeight < 48;
		const now = performance.now();
		if (bottom) stick.current = true;
		else if (now < intentUntil.current || (dragging.current && movedUp))
			stick.current = false;
		else if (stick.current && !hold.current) scrollToEnd();
		setAtBottom(stick.current || bottom);

		const report = onReadRef.current;
		if (!report || Math.abs(delta) <= 1) return;
		const dir = Math.sign(delta);
		const user =
			dragging.current ||
			now < (dir < 0 ? intentUntil.current : forwardUntil.current);
		if (bottom && dir > 0) {
			// 回到底部（含「回到底部」按钮与跟随新内容）：输入区属于这里
			travel.current = { dir: 0, px: 0 };
			report("forward");
			return;
		}
		if (!user || bottom) return;
		const prev = travel.current;
		travel.current =
			prev.dir === dir
				? { dir, px: prev.px + Math.abs(delta) }
				: { dir, px: Math.abs(delta) };
		if (travel.current.px >= READ_TRAVEL) {
			travel.current = { dir, px: 0 };
			report(dir < 0 ? "back" : "forward");
		}
	}, [scrollToEnd]);

	// 只观察、不拦截（被动监听）：哪些滚动出自用户往上 / 往下翻的动作
	useEffect(() => {
		const el = scrollRef.current;
		if (!el) return;
		const markUp = () => {
			intentUntil.current = performance.now() + INTENT_MS;
		};
		const markDown = () => {
			forwardUntil.current = performance.now() + INTENT_MS;
		};
		const onWheel = (e: WheelEvent) => {
			if (e.deltaY < 0) markUp();
			else if (e.deltaY > 0) markDown();
		};
		const onKey = (e: KeyboardEvent) => {
			if (UP_KEYS[e.key] || (e.key === " " && e.shiftKey)) markUp();
			else if (DOWN_KEYS[e.key] || e.key === " ") markDown();
		};
		const onTouchStart = (e: TouchEvent) => {
			touchY.current = e.touches[0]?.clientY ?? null;
		};
		const onTouchMove = (e: TouchEvent) => {
			const y = e.touches[0]?.clientY;
			if (y === undefined) return;
			// 手指往下拖 = 内容往上翻；手指往上推 = 往下翻
			if (touchY.current !== null && y > touchY.current + 2) markUp();
			else if (touchY.current !== null && y < touchY.current - 2) markDown();
			touchY.current = y;
		};
		const onPointerDown = (e: PointerEvent) => {
			// 直接按在滚动容器上（而不是某一行里）= 拖滚动条
			if (e.target === el) dragging.current = true;
		};
		const onPointerUp = () => {
			dragging.current = false;
		};
		const passive = { passive: true };
		el.addEventListener("wheel", onWheel, passive);
		el.addEventListener("keydown", onKey);
		el.addEventListener("touchstart", onTouchStart, passive);
		el.addEventListener("touchmove", onTouchMove, passive);
		el.addEventListener("pointerdown", onPointerDown);
		window.addEventListener("pointerup", onPointerUp);
		window.addEventListener("pointercancel", onPointerUp);
		return () => {
			el.removeEventListener("wheel", onWheel);
			el.removeEventListener("keydown", onKey);
			el.removeEventListener("touchstart", onTouchStart);
			el.removeEventListener("touchmove", onTouchMove);
			el.removeEventListener("pointerdown", onPointerDown);
			window.removeEventListener("pointerup", onPointerUp);
			window.removeEventListener("pointercancel", onPointerUp);
		};
	}, []);

	// 内容增长（新条目 / 流式增量 / 高度重测 / 输入坞变高）时，贴底则跟随
	// biome-ignore lint/correctness/useExhaustiveDependencies: items、total 与 insetBottom 变化即需要重新贴底
	useLayoutEffect(() => {
		if (stick.current && !hold.current) scrollToEnd();
	}, [items, total, insetBottom, scrollToEnd]);

	// 向前翻页：点击时记下距底距离，更早的条目并入后恢复它（内容从上方长出，阅读位置不动）
	const [loadingEarlier, setLoadingEarlier] = useState(false);
	const restore = useRef<{ fromBottom: number; count: number } | null>(null);
	const loadEarlier = useCallback(() => {
		const el = scrollRef.current;
		if (!el || !onLoadEarlier) return;
		restore.current = {
			fromBottom: el.scrollHeight - el.scrollTop,
			count: items.length,
		};
		stick.current = false;
		setLoadingEarlier(true);
		onLoadEarlier()
			.catch(() => {
				restore.current = null;
			})
			.finally(() => setLoadingEarlier(false));
	}, [onLoadEarlier, items.length]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: items、total 变化即需要检查是否该恢复位置
	useLayoutEffect(() => {
		const r = restore.current;
		const el = scrollRef.current;
		if (!r || !el || items.length === r.count) return;
		restore.current = null;
		el.scrollTop = el.scrollHeight - r.fromBottom;
	}, [items, total]);

	// biome-ignore lint/correctness/useExhaustiveDependencies: 只在挂载时定一次起始位置
	useEffect(() => {
		if (startAtTop) return;
		stick.current = true;
		scrollToEnd();
	}, [scrollToEnd]);

	// 定位某条条目（分享前的密钥扫描命中）：滚到该行居中、退出跟随，并短暂描边该行或对应工具卡
	const [revealed, setRevealed] = useState<{
		key: string;
		toolCallId?: string;
	} | null>(null);
	const itemsRef = useRef(items);
	itemsRef.current = items;
	useEffect(
		() =>
			registerRevealer(threadId, (target) => {
				// 主线不能对外分享，用不到定位；折叠后的显示行与条目也不再一一对应
				if (mainline) return false;
				const list = itemsRef.current;
				const hit = findRevealHit(
					list,
					getThreadStore(threadId).getState().entries,
					target,
				);
				const item = hit ? list[hit.index] : undefined;
				if (!hit || !item) return false;
				stick.current = false;
				setAtBottom(false);
				virtualizer.scrollToIndex(hit.index, { align: "center" });
				setRevealed({ key: item.key, toolCallId: hit.toolCallId });
				return true;
			}),
		[threadId, virtualizer, mainline],
	);
	useEffect(() => {
		if (!revealed) return;
		const id = revealed.toolCallId;
		let card: HTMLElement | null = null;
		let frame = 0;
		let tries = 0;
		// 工具卡在行挂载、测高之后才在 DOM 里：逐帧找，最多 REVEAL_TRIES 帧
		const findCard = () => {
			if (!id) return;
			card =
				scrollRef.current?.querySelector<HTMLElement>(
					`[data-tool-call-id="${id.replace(/["\\]/g, "\\$&")}"]`,
				) ?? null;
			if (card) {
				card.scrollIntoView?.({ block: "center" });
				card.classList.add(...REVEAL_RING);
			} else if (++tries < REVEAL_TRIES)
				frame = requestAnimationFrame(findCard);
		};
		frame = requestAnimationFrame(findCard);
		const timer = setTimeout(() => setRevealed(null), REVEAL_MS);
		return () => {
			cancelAnimationFrame(frame);
			clearTimeout(timer);
			card?.classList.remove(...REVEAL_RING);
		};
	}, [revealed]);

	const virtualItems = virtualizer.getVirtualItems();
	const rendered = useMemo(
		() =>
			virtualItems.map((v) => {
				const item = v.index < rows.length ? rows[v.index] : undefined;
				const turn = turns.footers.get(v.index);
				const quote = mainline && turn ? turnFinalText(turn) : "";
				// 会话分隔线与 agent 各块都铺满正文列；用户气泡右侧让出头像列（pr-10）
				const userRow = item !== undefined && isUserRow(item);
				return (
					<div
						key={v.key}
						data-index={v.index}
						data-kind={item?.kind}
						data-role={
							item?.kind === "message" ? String(item.message.role) : undefined
						}
						data-entry-id={item?.kind === "message" ? item.entryId : undefined}
						data-live={item?.kind === "message" && item.live ? "1" : undefined}
						ref={virtualizer.measureElement}
						// 行内菜单（消息操作）会溢出到相邻行：悬停 / 聚焦时把本行抬到兄弟行之上；
						// 行是 inline-size 容器：助手正文的表格按行宽（100cqw）伸出正文列
						className="@container absolute left-0 w-full px-3 focus-within:z-10 hover:z-10 sm:px-5"
						style={{
							transform: `translateY(${v.start}px)`,
							// 行间距只来自本行上边距（见 rowGaps）；末尾的 footer 行再留一点底
							paddingTop: item ? gaps[v.index] : GAP_FLOW,
							paddingBottom: item ? 0 : 6,
						}}
					>
						<div
							className={`group/row relative mx-auto max-w-3xl ${userRow ? "pr-10" : ""} ${revealed && !revealed.toolCallId && item?.key === revealed.key ? REVEAL_RING.join(" ") : ""}`}
						>
							{turns.starts.has(v.index) && (
								// 回合起点：头像单独占一行，放在本行（回合首行）内部，不另占虚拟行
								<TurnHead
									working={liveStart === v.index}
									model={turns.models.get(v.index)}
									// 行里没有 thinking_level_change 时，各回合的级别就是会话当前级别
									thinking={
										turns.models.get(v.index)?.thinking ??
										(turns.thinkingSeen ? undefined : sessionThinking)
									}
									known={sessionModel}
									face={resolveTurnRole(
										turns.models.get(v.index),
										turns.trails.get(v.index),
										identities.faces,
									)}
									identities={identities}
									source={turns.trails.get(v.index)?.record?.source}
								/>
							)}
							{item ? (
								// 模型 / 思考级别切换行：保留虚拟行（行号不变），不画内容
								turns.hidden.has(v.index) ? null : (
									<>
										<ItemView
											item={item}
											turn={turn}
											turnStart={
												turns.starts.has(v.index) ||
												turns.userStarts.has(v.index)
											}
										/>
										{quote && <ContinueInNew text={quote} />}
									</>
								)
							) : (
								<>
									{pendingPrompts.map((p) => (
										<PendingPrompt key={p.id} text={p.text} />
									))}
									{footer}
								</>
							)}
						</div>
					</div>
				);
			}),
		[
			virtualItems,
			rows,
			mainline,
			footer,
			virtualizer.measureElement,
			turns,
			gaps,
			pendingPrompts,
			liveStart,
			sessionModel,
			sessionThinking,
			identities,
			revealed,
		],
	);

	// 「回到底部」是悬在托盘顶边上方 8px 的独立小圆钮；输入坞整体独立悬浮，不遮罩、不淡出周围正文
	const trayTop = trayTopOverride ?? Math.max(0, insetBottom - DOCK_GAP);

	return (
		<div className="relative min-h-0 flex-1">
			{/* 只拦纵向的滚动链：横向要能传到根滚动（iOS 下沉侧栏的原生滚动模式靠它拖开卡片） */}
			<div
				ref={setScrollEl}
				onScroll={onScroll}
				className="h-full overflow-y-auto overscroll-y-contain"
				style={{
					scrollPaddingTop: insetTop,
					scrollPaddingBottom: insetBottom,
				}}
				data-testid="transcript"
			>
				<div
					ref={contentRef}
					className="relative w-full"
					style={{ height: total }}
				>
					{hasEarlier && (
						<div
							className="absolute left-0 flex w-full items-center justify-center"
							style={{ top: insetTop, height: EARLIER_H }}
						>
							<button
								type="button"
								disabled={loadingEarlier}
								onClick={loadEarlier}
								className="glass rounded-full px-3 py-1 text-muted text-sm hover:text-fg disabled:opacity-60"
							>
								{t("transcript.loadEarlier")}
							</button>
						</div>
					)}
					<ActiveToolCallsContext.Provider value={activeTools}>
						{rendered}
					</ActiveToolCallsContext.Provider>
					{selection.quote && !quoteBar && (
						<QuoteChip
							quote={selection.quote}
							onTake={selection.take}
							onExplain={selection.explain}
						/>
					)}
				</div>
			</div>
			{quoteBar && selection.quote && (
				<QuoteBar
					quote={selection.quote}
					bottom={trayTop + 8}
					onTake={selection.take}
					onExplain={selection.explain}
					onDismiss={selection.dismiss}
				/>
			)}
			{!atBottom && !quoteBar && (
				// 水平居中、悬在托盘顶边上方留白里的小圆钮
				<button
					type="button"
					aria-label={t("transcript.jumpToEnd")}
					title={t("transcript.jumpToEnd")}
					className="glass pop-in absolute left-1/2 z-20 inline-grid h-7 w-7 -translate-x-1/2 place-items-center rounded-full text-muted hover:text-fg"
					style={{ bottom: trayTop + 8 }}
					onClick={() => {
						stick.current = true;
						setAtBottom(true);
						scrollToEnd();
					}}
				>
					<Icon name="arrowDown" size={14} />
				</button>
			)}
		</div>
	);
});
