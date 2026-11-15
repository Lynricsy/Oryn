/**
 * 匿名分享页 `/s/<slug>`（M5 计划 5.2）：L0 桌面上一张居中的纸面，页头（品牌标 + 标题 + 模式徽标 + 复制 / Markdown）
 * 盖在只读转录上。数据只来自公开接口（`share/feed.ts`）：不挂 AppShell、不调 useMe、不连私有 realtime。
 * 资源地址经 `AssetUrlContext` 换成公开接口；转录上下文 `readOnly`，没有输入坞、右面板与侧栏。
 * 回合头身份（M9.6）与 task 卡片的子 agent 头像（M13 R9）取自匿名身份接口（`share/identity.ts`），回合头判定与站内
 * 同一个函数（`lib/turn-role.ts`），接口里没有的一律默认品牌标 / 默认字形。转录子树挂一个独立的 QueryClient，其中的
 * 查询一律不发请求（用户头像等单用户设置按默认显示）。
 */
import {
	QueryClient,
	QueryClientProvider,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router";
import type { Entry } from "../api/client";
import { identityKeys } from "../api/identity-queries";
import { qk } from "../api/queries";
import { HEADER_VEIL } from "../components/thread/ThreadHeader";
import {
	TranscriptContext,
	type TranscriptContextValue,
	type TurnIdentities,
} from "../components/transcript/context";
import { textOfContent } from "../components/transcript/MessageView";
import { Transcript } from "../components/transcript/Transcript";
import { WorkingFooter } from "../components/transcript/WorkingFooter";
import { ActionError } from "../components/ui/ActionError";
import { CopyButton } from "../components/ui/CopyButton";
import {
	Badge,
	BrandMark,
	Sheet,
	StatusDot,
	TitleGlyph,
} from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import { Button, EmptyState, Spinner } from "../components/ui/primitives";
import { AssetUrlContext, type AssetUrls } from "../lib/asset-urls";
import { parseAttachmentBlock } from "../lib/attachments";
import { formatDateTime } from "../lib/format";
import { parseTitleCard } from "../lib/title";
import { advanceTrail, EMPTY_TRAIL } from "../lib/turn-role";
import { useBreakpoint } from "../lib/viewport";
import {
	fetchShare,
	type PublicShare,
	ShareFeed,
	ShareGoneError,
	shareKey,
} from "../share/feed";
import { shareIdentityKey, useShareIdentity } from "../share/identity";
import { useThread } from "../state/stores";
import { buildTranscript, isMainFile } from "../state/transcript";
import { isRecord } from "../tool-render/util";

const SLUG_RE = /^[0-9a-f]{32}$/;
/** 转录里有尚未公开的附件时，多久复查一次公开元数据（所有者可能刚发布了它）。 */
export const ATTACHMENT_POLL_MS = 30_000;

const shareQueryKey = (slug: string) => ["public-share", slug] as const;

/** 公开元数据查询（页面与转录视图共用同一个 key 与选项）。 */
function shareQuery(slug: string) {
	return {
		queryKey: shareQueryKey(slug),
		queryFn: () => fetchShare(slug),
		retry: (n: number, e: unknown) => !(e instanceof ShareGoneError) && n < 2,
		staleTime: Number.POSITIVE_INFINITY,
	};
}

/** 转录里用户消息附件块引用的附件 id（按出现顺序去重）。 */
function referencedAttachments(entries: readonly Entry[]): string[] {
	const ids = new Set<string>();
	for (const e of entries) {
		const p = e.payload as { message?: { role?: unknown; content?: unknown } };
		if (p?.message?.role !== "user") continue;
		for (const a of parseAttachmentBlock(textOfContent(p.message.content))
			.attachments)
			if (a.id) ids.add(a.id);
	}
	return [...ids];
}

const noop = () => {};

/**
 * 预置数据的更新时刻往后推 20 天：钩子自带的 `staleTime`（5 分钟）也不会让它在页面存续期内过期重取；
 * 不能更远——观察者按「距过期的时间」设定时器，超过 2^31 毫秒会溢出成立即触发。
 */
const NEVER_STALE_MS = 20 * 86_400_000;

/**
 * 转录子树专用的 QueryClient：所有查询默认不启用——转录里的头像读的是私有的单用户设置，
 * 匿名页不得触达；预置空身份，用户头像退到默认字形。回合头身份另由上下文的 `identities` 给出。
 */
function isolatedClient(): QueryClient {
	const client = new QueryClient({
		defaultOptions: {
			queries: {
				enabled: false,
				retry: false,
				staleTime: Number.POSITIVE_INFINITY,
			},
		},
	});
	const updated = { updatedAt: Date.now() + NEVER_STALE_MS };
	for (const key of [
		identityKeys.user,
		identityKeys.agent,
		identityKeys.assistant,
	])
		client.setQueryData(key, {}, updated);
	client.setQueryData(identityKeys.roles, { roles: [] }, updated);
	client.setQueryData(qk.gitIdentity, {}, updated);
	return client;
}

/**
 * 转录里「生效期间出现过消息」的来源记录的不透明键（role / alias，按出现顺序去重；只看主会话文件）。
 * 与后端 transcript.UsedRoles 同一规则：fallback / prewalk 是临时覆盖、不截断记录，其它 model_change 让记录失效，
 * manual 记录不算。新 Core 在第一条回复前就把来源记录落盘：记录刚到、还没有消息时身份接口合法地不含该 role，
 * 这时重取只会拿到空表并把键标成「已问过」，之后消息到了也不会再取——所以只为已用到的键重取。
 */
export function usedSourceKeys(entries: readonly Entry[]): string[] {
	const keys = new Set<string>();
	let trail = EMPTY_TRAIL;
	let used = false;
	const flush = () => {
		const r = trail.record;
		if (r && used && r.source !== "manual") {
			if (r.role) keys.add(r.role);
			if (r.alias) keys.add(r.alias);
		}
		used = false;
	};
	for (const e of entries) {
		if (!isMainFile(e.file)) continue;
		const next = advanceTrail(trail, e);
		if (next.record !== trail.record) flush();
		trail = next;
		if (e.type === "message" && trail.record) used = true;
	}
	flush();
	return [...keys];
}

/** 没写 agent 的 task 调用由 omp 交给的子 agent（与后端 transcript.DefaultTaskAgent 一致）。 */
const DEFAULT_TASK_AGENT = "task";

/**
 * 主会话里 `task` 工具调用用到的子 agent 名（去重、按首次出现排序）。与后端 transcript.UsedAgents 同一规则：
 * 认批量 `{tasks: [{agent}]}` 与扁平 `{agent}` 两种形状，条目没写时退回顶层 agent，都没写算 `task`。
 */
export function usedAgentNames(entries: readonly Entry[]): string[] {
	const names = new Set<string>();
	for (const e of entries) {
		if (e.type !== "message" || !isMainFile(e.file)) continue;
		const message = e.payload.message;
		const content = isRecord(message) ? message.content : undefined;
		if (!Array.isArray(content)) continue;
		for (const c of content) {
			if (!isRecord(c) || c.type !== "toolCall" || c.name !== "task") continue;
			const args = isRecord(c.arguments) ? c.arguments : {};
			const top = typeof args.agent === "string" ? args.agent : "";
			const tasks = Array.isArray(args.tasks) ? args.tasks : [];
			if (tasks.length === 0) names.add(top || DEFAULT_TASK_AGENT);
			for (const t of tasks) {
				const own = isRecord(t) && typeof t.agent === "string" ? t.agent : "";
				names.add(own || top || DEFAULT_TASK_AGENT);
			}
		}
	}
	return [...names];
}

/**
 * 实时分享的线程状态：工作中沿用侧栏的转圈（中性色，绿 / 黄 / 红只表示成功 / 警告 / 错误），
 * 出错为红点，其余中性点。
 */
function LiveState({ state }: { state: string }): ReactNode {
	const { t } = useTranslation();
	const label = t(`threadState.${state as "idle"}`);
	if (state === "working")
		return (
			<span
				role="img"
				aria-label={label}
				title={label}
				className="inline-flex shrink-0 text-muted"
			>
				<Spinner size={11} />
			</span>
		);
	return (
		<StatusDot tone={state === "error" ? "err" : "neutral"} label={label} />
	);
}

function Frame({ children }: { children: ReactNode }): ReactNode {
	const bp = useBreakpoint();
	const inset = bp !== "compact";
	return (
		<div
			className={`shell-wash flex h-dvh w-full justify-center text-fg ${inset ? "p-2 sm:p-3" : ""}`}
		>
			{/* 纸面不做圆角裁剪：页头纱的 backdrop-filter 要在圆角 overflow 之外（同线程页） */}
			<Sheet
				inset={inset}
				className="relative flex h-full w-full min-w-0 max-w-[880px] flex-col overflow-visible pt-safe"
			>
				{children}
			</Sheet>
		</div>
	);
}

function Gone(): ReactNode {
	const { t } = useTranslation();
	return (
		<Frame>
			<div className="flex flex-1 items-center justify-center">
				<EmptyState icon="link" title={t("share.page.gone")}>
					{t("share.page.goneHint")}
				</EmptyState>
			</div>
		</Frame>
	);
}

function ShareHeader({
	slug,
	share,
	title,
	state,
}: {
	slug: string;
	share: PublicShare;
	title: string;
	state?: string;
}): ReactNode {
	const { t } = useTranslation();
	const card = parseTitleCard(title);
	const text = card.text || t("thread.untitled");
	return (
		<header className="veil-host relative isolate @container/header flex h-12 shrink-0 items-center gap-2 px-3 sm:px-4">
			<div aria-hidden className="veil" style={{ height: HEADER_VEIL }}>
				<i />
				<i />
				<i />
				<i />
				<i />
				<i />
			</div>
			<BrandMark size={18} />
			{card.icon && <TitleGlyph icon={card.icon} seed={card.text} size={18} />}
			<h1
				className="min-w-0 truncate font-semibold text-sm tracking-tight"
				title={text}
			>
				{text}
			</h1>
			{share.mode === "snapshot" ? (
				<Badge icon="camera" title={formatDateTime(share.sharedAt)}>
					<span className="@md/header:hidden">{t("share.mode.snapshot")}</span>
					<span className="hidden @md/header:inline">
						{t("share.page.snapshotBadge", {
							date: formatDateTime(share.sharedAt),
						})}
					</span>
				</Badge>
			) : (
				<span className="inline-flex shrink-0 items-center gap-1.5">
					<Badge icon="broadcast">{t("share.mode.live")}</Badge>
					{state && <LiveState state={state} />}
				</span>
			)}
			<span className="min-w-0 flex-1" />
			<span className="hidden @sm/header:inline-flex">
				<CopyButton text={location.href} label={t("share.page.copyLink")} />
			</span>
			<span className="inline-flex @sm/header:hidden">
				<CopyButton text={location.href} ariaLabel={t("share.page.copyLink")} />
			</span>
			<a
				href={`/s/${slug}.md`}
				target="_blank"
				rel="noopener"
				className="inline-flex h-6 shrink-0 items-center gap-1 rounded-sm px-1.5 text-faint text-xs transition-colors duration-150 hover:bg-hover hover:text-fg"
			>
				<Icon name="fileText" size={13} aria-hidden />
				{t("share.page.markdown")}
			</a>
		</header>
	);
}

function ShareView({
	slug,
	share,
}: {
	slug: string;
	share: PublicShare;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const key = shareKey(slug);
	const ready = useThread(key, (s) => s.snapshotReceived);
	const entries = useThread(key, (s) => s.entries);
	const leaf = useThread(key, (s) => s.leafEntryId);
	const liveMessage = useThread(key, (s) => s.liveMessage);
	const liveTools = useThread(key, (s) => s.liveTools);
	const liveToolOrder = useThread(key, (s) => s.liveToolOrder);
	const title = useThread(key, (s) => s.thread?.title) ?? share.title;
	const state = useThread(key, (s) => s.thread?.state);
	const live = share.mode === "live";
	const [client] = useState(isolatedClient);
	const qc = useQueryClient();

	// 附件的公开地址随元数据更新：转录里出现元数据里没有的附件 → 立即复查一次；
	// 还有未公开的附件 → 定期复查（所有者可能之后才发布它）。未公开的附件始终不给下载地址
	const referenced = useMemo(() => referencedAttachments(entries), [entries]);
	const unknown = referenced
		.filter((id) => !share.attachments.some((a) => a.id === id))
		.join(",");
	const pending = referenced.some(
		(id) => !share.attachments.find((a) => a.id === id)?.url,
	);
	useQuery({
		...shareQuery(slug),
		refetchInterval: pending ? ATTACHMENT_POLL_MS : false,
	});
	useEffect(() => {
		if (unknown)
			void qc.refetchQueries({ queryKey: shareQueryKey(slug), exact: true });
	}, [unknown, qc, slug]);

	// 回合头与 task 卡片的身份：匿名身份接口（快照固化、实时现算）。实时分享里出现身份表没有、且已有消息用到的
	// 来源键（刚点了一个新 role 并发了消息）或 task 调用用到的 agent，就重取一次，每个键 / agent 至多一次
	// （用到但没设头像的 role / agent 本来就不在表里）
	const identity = useShareIdentity(slug).data;
	const keys = useMemo(() => usedSourceKeys(entries), [entries]);
	const agentNames = useMemo(() => usedAgentNames(entries), [entries]);
	const asked = useRef(new Set<string>());
	useEffect(() => {
		if (!live || !identity) return;
		const fresh = [
			...keys
				.filter((k) => !identity.roles.some((r) => r.key === k))
				.map((k) => `role:${k}`),
			...agentNames
				.filter((n) => !identity.agents?.some((a) => a.name === n))
				.map((n) => `agent:${n}`),
		].filter((k) => !asked.current.has(k));
		if (fresh.length === 0) return;
		for (const k of fresh) asked.current.add(k);
		void qc.invalidateQueries({
			queryKey: shareIdentityKey(slug),
			exact: true,
		});
	}, [live, identity, keys, agentNames, qc, slug]);
	const identities = useMemo<TurnIdentities>(() => {
		const agent = identity?.agent;
		const name = agent?.name?.trim() || undefined;
		const avatar = {
			avatarIcon: agent?.avatarIcon || undefined,
			avatarImage: agent?.avatarImage || undefined,
		};
		const has = !!(name || avatar.avatarIcon || avatar.avatarImage);
		return {
			faces: (identity?.roles ?? []).map((r) => ({
				key: r.key,
				role: r.role,
				name: r.name,
				avatarIcon: r.avatarIcon,
				avatarImage: r.avatarImage,
			})),
			fallback: has ? { name, avatar } : undefined,
			fallbackKind: has ? "agent" : "default",
		};
	}, [identity]);

	useEffect(() => {
		const prev = document.title;
		document.title = `${parseTitleCard(title).text || t("thread.untitled")} · Oryn`;
		return () => {
			document.title = prev;
		};
	}, [title, t]);

	const model = useMemo(
		() =>
			buildTranscript({
				entries,
				leafEntryId: leaf,
				liveMessage,
				liveTools,
				liveToolOrder,
				// 分享页不显示站内提示（重试、缓存预热等）
				notices: [],
			}),
		[entries, leaf, liveMessage, liveTools, liveToolOrder],
	);
	const agentFaces = identity?.agents;
	const ctx: TranscriptContextValue = useMemo(
		() => ({
			threadId: key,
			toolResults: model.toolResults,
			liveTools: model.liveTools,
			subagents: {},
			openSubagent: noop,
			forkFrom: noop,
			labelEntry: noop,
			readOnly: true,
			identities,
			agentFaces,
		}),
		[key, model.toolResults, model.liveTools, identities, agentFaces],
	);
	const assets: AssetUrls = useMemo(
		() => ({
			blob: (sha) =>
				`/api/v1/public/shares/${slug}/blobs/${encodeURIComponent(sha)}`,
			attachment: (id) =>
				share.attachments.find((a) => a.id === id)?.url ?? null,
		}),
		[slug, share.attachments],
	);

	return (
		<Frame>
			<QueryClientProvider client={client}>
				<AssetUrlContext.Provider value={assets}>
					<TranscriptContext.Provider value={ctx}>
						<div
							className={`flex min-h-0 flex-1 flex-col overflow-hidden ${bp !== "compact" ? "rounded-[calc(var(--r-xl)-1px)]" : ""}`}
						>
							{ready ? (
								<Transcript
									items={model.items}
									insetTop={HEADER_VEIL}
									startAtTop={!live}
									footer={
										<>
											{live && <WorkingFooter threadId={key} />}
											<p className="flex items-center justify-center gap-1.5 pt-6 pb-4 text-2xs text-faint">
												<BrandMark size={12} />
												{t("share.page.footer")}
											</p>
										</>
									}
								/>
							) : (
								<div className="flex flex-1 items-center justify-center text-muted">
									<Spinner size={18} />
								</div>
							)}
						</div>
					</TranscriptContext.Provider>
				</AssetUrlContext.Provider>
			</QueryClientProvider>
			<div className="absolute inset-x-0 top-0 z-30 pt-safe">
				<ShareHeader
					slug={slug}
					share={share}
					title={title}
					state={live ? (state ?? share.state) : undefined}
				/>
			</div>
		</Frame>
	);
}

export function SharePage(): ReactNode {
	const { slug = "" } = useParams();
	const { t } = useTranslation();
	const qc = useQueryClient();
	const valid = SLUG_RE.test(slug);
	const q = useQuery({ ...shareQuery(slug), enabled: valid });
	const [goneSlug, setGoneSlug] = useState<string | null>(null);
	const share = q.data;
	const loaded = share !== undefined;
	const gone = !valid || goneSlug === slug || q.error instanceof ShareGoneError;

	// 数据源只随 slug 建一次：元数据之后的刷新（附件的公开地址）不重连；链接失效即停
	useEffect(() => {
		const initial = qc.getQueryData<PublicShare>(shareQueryKey(slug));
		if (!loaded || gone || !initial) return;
		const feed = new ShareFeed(
			slug,
			initial,
			() => setGoneSlug(slug),
			// 断线重连时（复核链接成功）：元数据可能变了；实时分享的身份跟随当前设置，也重取一次
			(fresh) => {
				qc.setQueryData(shareQueryKey(slug), fresh);
				if (fresh.mode === "live")
					void qc.invalidateQueries({
						queryKey: shareIdentityKey(slug),
						exact: true,
					});
			},
		);
		feed.start();
		return () => feed.stop();
	}, [slug, loaded, gone, qc]);

	if (gone) return <Gone />;
	// 已有内容时后台复查失败（非 404）不打断阅读
	if (q.error && !share)
		return (
			<Frame>
				<div className="flex flex-1 flex-col items-center justify-center gap-3 p-6">
					<ActionError step="shareLoad" error={q.error} />
					<Button size="sm" onClick={() => void q.refetch()}>
						{t("share.page.retry")}
					</Button>
				</div>
			</Frame>
		);
	if (!share)
		return (
			<Frame>
				<div className="flex flex-1 items-center justify-center text-muted">
					<Spinner size={18} />
				</div>
			</Frame>
		);
	return <ShareView slug={slug} share={share} />;
}
