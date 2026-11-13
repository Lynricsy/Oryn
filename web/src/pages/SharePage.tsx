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
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router";
import { qk } from "../api/queries";
import { HEADER_VEIL } from "../components/thread/ThreadHeader";
import {
	TranscriptContext,
	type TranscriptContextValue,
} from "../components/transcript/context";
import { Transcript } from "../components/transcript/Transcript";
import { WorkingFooter } from "../components/transcript/WorkingFooter";
import { CopyButton } from "../components/ui/CopyButton";
import {
	Badge,
	BrandMark,
	Sheet,
	StatusDot,
	TitleGlyph,
	type Tone,
} from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	Spinner,
} from "../components/ui/primitives";
import { AssetUrlContext, type AssetUrls } from "../lib/asset-urls";
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

/** 实时分享的状态点：工作中脉动（绿 = 正在直播），出错为红，其余中性。 */
function stateTone(state: string | undefined): Tone {
	if (state === "working") return "ok";
	if (state === "error") return "err";
	return "neutral";
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
					{state && (
						<StatusDot
							tone={stateTone(state)}
							pulse={state === "working"}
							label={t(`threadState.${state as "idle"}`)}
						/>
					)}
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
	const valid = SLUG_RE.test(slug);
	const q = useQuery({
		queryKey: ["public-share", slug],
		queryFn: () => fetchShare(slug),
		enabled: valid,
		retry: (n, e) => !(e instanceof ShareGoneError) && n < 2,
		staleTime: Number.POSITIVE_INFINITY,
	});
	const [gone, setGone] = useState(false);
	const share = q.data;

	useEffect(() => {
		if (!share) return;
		setGone(false);
		const feed = new ShareFeed(slug, share, () => setGone(true));
		feed.start();
		return () => feed.stop();
	}, [slug, share]);

	if (!valid || gone || q.error instanceof ShareGoneError) return <Gone />;
	if (q.error)
		return (
			<Frame>
				<div className="flex flex-1 flex-col items-center justify-center gap-3 p-6">
					<ErrorText error={q.error} />
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
