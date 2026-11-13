/**
 * 匿名分享页 `/s/<slug>`（M5 计划 5.2）：L0 桌面上一张居中的纸面，页头（品牌标 + 标题 + 模式徽标 + 复制 / Markdown）
 * 盖在只读转录上。数据只来自公开接口（`share/feed.ts`）：不挂 AppShell、不调 useMe、不连私有 realtime。
 * 资源地址经 `AssetUrlContext` 换成公开接口；转录上下文 `readOnly`，没有输入坞、右面板与侧栏。
 * 转录子树挂一个独立的 QueryClient，其中的查询一律不发请求（头像等单用户设置按默认显示）。
 */
import {
	QueryClient,
	QueryClientProvider,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router";
import type { Entry } from "../api/client";
import { qk } from "../api/queries";
import { HEADER_VEIL } from "../components/thread/ThreadHeader";
import {
	TranscriptContext,
	type TranscriptContextValue,
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
import { useBreakpoint } from "../lib/viewport";
import {
	fetchShare,
	type PublicShare,
	ShareFeed,
	ShareGoneError,
	shareKey,
} from "../share/feed";
import { useThread } from "../state/stores";
import { buildTranscript } from "../state/transcript";

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
 * 转录子树专用的 QueryClient：所有查询默认不启用——转录里的头像读的是私有的单用户设置，
 * 匿名页不得触达；预置空身份，用户 / agent 头像退到默认字形。
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
	client.setQueryData(qk.userIdentity, {});
	client.setQueryData(qk.agentIdentity, {});
	client.setQueryData(qk.gitIdentity, {});
	return client;
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
		}),
		[key, model.toolResults, model.liveTools],
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
			(fresh) => qc.setQueryData(shareQueryKey(slug), fresh),
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
