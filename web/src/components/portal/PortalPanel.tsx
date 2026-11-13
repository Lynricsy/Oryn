/**
 * Portal 标签（计划 M4 5.2）：顶部工具条（后退 / 前进 / 刷新、锁 + URL 胶囊、链接选择、复制、新窗口、齿轮 → 访问弹层），
 * 下方内嵌井里的 iframe 显示 orb 上受管服务的 portal。
 *
 * 导航模型：面板维护一个「工具条历史」栈（初始 = 所选 portal 首个链接的路径），从链接菜单选择或在胶囊里回车时入栈；
 * 后退 / 前进在栈内移动，刷新 = 以同一地址重建 iframe。iframe 内部的跳转跨源读不到，不进这个栈。
 * iframe 先到 portal 主机的 `/__oryn/login?next=<路径>`（所有者直接回跳，否则走主域名授权握手）。
 * orb 冻结时不做遮罩：iframe 的请求自己会唤醒 orb 并显示过渡页。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { formatDateTime } from "../../lib/format";
import { useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { useOrbOf } from "../thread/ThreadHeader";
import { CopyButton } from "../ui/CopyButton";
import { Icon } from "../ui/icons";
import { EmptyState, IconButton, Menu, type MenuItem } from "../ui/primitives";
import { AccessPopover, activeShare, useNow } from "./AccessPopover";
import {
	firstLinkPath,
	formatRemaining,
	normalizePath,
	portalFrameSrc,
	portalHost,
	portalUrl,
} from "./portal-url";

type OrbPortal = Schemas["OrbPortal"];

/**
 * 被嵌页面的沙箱：不含 `allow-top-navigation`（不能把 Oryn 整页带走）；`allow-same-origin` 是安全的，
 * 因为 portal 主机与主站不同源。
 */
export const PORTAL_SANDBOX =
	"allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads";

/** 工具条历史里的一站：端口 + 路径。 */
interface Stop {
	port: number;
	path: string;
}

interface History {
	stops: Stop[];
	index: number;
}

/** 从 portal 的首个链接开始的新历史。 */
function startAt(portal: OrbPortal): History & { first: Stop } {
	const first = { port: portal.port, path: firstLinkPath(portal) };
	return { stops: [first], index: 0, first };
}

/** 链接菜单：全部 portal 的链接，按 `folder` 分组成子菜单；多个端口时提示端口号。 */
function linkItems(
	portals: readonly OrbPortal[],
	go: (stop: Stop) => void,
): MenuItem[] {
	const many = portals.length > 1;
	const top: MenuItem[] = [];
	const folders = new Map<string, MenuItem[]>();
	for (const p of portals)
		p.links.forEach((link, i) => {
			const item: MenuItem = {
				key: `${p.port}:${i}`,
				label: link.title,
				hint: many ? `:${p.port}` : normalizePath(link.path),
				onSelect: () => go({ port: p.port, path: normalizePath(link.path) }),
			};
			if (!link.folder) top.push(item);
			else
				folders.set(link.folder, [...(folders.get(link.folder) ?? []), item]);
		});
	return [
		...top,
		...[...folders].map(([folder, children]) => ({
			key: `folder:${folder}`,
			label: folder,
			icon: "folder" as const,
			children,
		})),
	];
}

function PortalView({
	threadId,
	orbId,
	template,
	portals,
	portal,
}: {
	threadId: string;
	orbId: string;
	template: string;
	portals: readonly OrbPortal[];
	portal: OrbPortal;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const now = useNow(30_000);
	const [hist, setHist] = useState<History>(() => startAt(portal));
	// 每次导航 / 刷新递增，用作 iframe 的 key（同一地址也重新加载）
	const [nonce, setNonce] = useState(0);
	// 胶囊里正在编辑的路径；null = 显示当前路径
	const [draft, setDraft] = useState<string | null>(null);

	let stop = hist.stops[hist.index];
	// 端口被外部改了（信息面板的「打开」、所选 portal 消失）：历史从该 portal 的首个链接重新开始
	if (!stop || stop.port !== portal.port) {
		const fresh = startAt(portal);
		setHist(fresh);
		stop = fresh.first;
	}

	const moveTo = (next: History) => {
		const target = next.stops[next.index];
		if (!target) return;
		setHist(next);
		setNonce((n) => n + 1);
		setDraft(null);
		if (target.port !== portal.port)
			uiStore.getState().setPortalPort(threadId, target.port);
	};
	const go = (target: Stop) =>
		moveTo({
			stops: [...hist.stops.slice(0, hist.index + 1), target],
			index: hist.index + 1,
		});

	const pub = activeShare(portal, now);
	const linkTitle =
		portal.links.find((l) => normalizePath(l.path) === stop.path)?.title ??
		portal.service;
	const host = portalHost(template, stop.port);
	const totalLinks = portals.reduce((n, p) => n + p.links.length, 0);

	return (
		<div className="flex h-full min-h-0 flex-col" data-testid="portal-panel">
			<div
				role="toolbar"
				aria-label={t("portal.toolbar")}
				className="flex h-11 shrink-0 items-center gap-0.5 border-border border-b px-2"
			>
				<IconButton
					icon="back"
					box="sm"
					size={15}
					label={t("portal.back")}
					disabled={hist.index === 0}
					onClick={() => moveTo({ ...hist, index: hist.index - 1 })}
				/>
				<IconButton
					icon="forward"
					box="sm"
					size={15}
					label={t("portal.forward")}
					disabled={hist.index >= hist.stops.length - 1}
					onClick={() => moveTo({ ...hist, index: hist.index + 1 })}
				/>
				<IconButton
					icon="refresh"
					box="sm"
					size={15}
					label={t("portal.reload")}
					onClick={() => setNonce((n) => n + 1)}
				/>
				<form
					className="mx-1 flex h-7 min-w-0 flex-1 items-center gap-1.5 rounded-full border border-border bg-inset pr-2.5 pl-2.5 text-xs transition-[border-color,box-shadow] duration-150 focus-within:border-accent focus-within:ring-3 focus-within:ring-accent/20"
					onSubmit={(e) => {
						e.preventDefault();
						if (draft !== null)
							go({ port: stop.port, path: normalizePath(draft) });
					}}
				>
					{pub ? (
						<span
							data-testid="portal-public"
							title={t("portal.publicUntil", {
								at: formatDateTime(pub.expiresAt),
							})}
							className="inline-flex shrink-0 items-center gap-1 text-muted"
						>
							<Icon name="globe" size={13} aria-hidden />
							<span className="text-2xs tabular-nums">
								{formatRemaining(pub.expiresAt - now, i18n.language)}
							</span>
							<span className="sr-only">{t("portal.publicTitle")}</span>
						</span>
					) : (
						<span
							data-testid="portal-private"
							title={t("portal.private")}
							className="inline-flex shrink-0 text-faint"
						>
							<Icon name="lock" size={13} aria-hidden />
							<span className="sr-only">{t("portal.private")}</span>
						</span>
					)}
					<span
						data-testid="portal-host"
						title={host}
						className="min-w-0 max-w-[60%] shrink truncate text-faint"
					>
						{host}
					</span>
					<input
						aria-label={t("portal.path")}
						spellCheck={false}
						autoCapitalize="off"
						autoCorrect="off"
						className="h-full min-w-12 flex-1 bg-transparent text-fg outline-none focus-visible:outline-none"
						value={draft ?? stop.path}
						onChange={(e) => setDraft(e.target.value)}
						onBlur={() => setDraft(null)}
						onKeyDown={(e) => {
							if (e.key === "Escape") {
								setDraft(null);
								e.currentTarget.blur();
							}
						}}
					/>
				</form>
				{totalLinks > 1 && (
					<Menu
						items={linkItems(portals, go)}
						trigger={(p) => (
							<IconButton
								icon="link"
								box="sm"
								size={15}
								label={t("portal.links")}
								{...p}
							/>
						)}
					/>
				)}
				<span className="inline-flex h-7 w-7 shrink-0 items-center justify-center">
					<CopyButton
						text={portalUrl(template, stop.port, stop.path)}
						ariaLabel={t("portal.copyUrl")}
					/>
				</span>
				<IconButton
					icon="external"
					box="sm"
					size={15}
					label={t("portal.openNew")}
					onClick={() =>
						window.open(
							portalFrameSrc(template, stop.port, stop.path),
							"_blank",
							"noopener",
						)
					}
				/>
				<AccessPopover orbId={orbId} portal={portal} />
			</div>
			<div className="min-h-0 flex-1 p-2">
				<div className="h-full overflow-hidden rounded-lg border border-border bg-inset">
					<iframe
						key={nonce}
						data-testid="portal-frame"
						title={t("portal.frameTitle", { title: linkTitle })}
						src={portalFrameSrc(template, stop.port, stop.path)}
						sandbox={PORTAL_SANDBOX}
						allow="clipboard-read; clipboard-write; fullscreen"
						referrerPolicy="no-referrer"
						className="block h-full w-full border-0"
					/>
				</div>
			</div>
		</div>
	);
}

export default function PortalPanel({
	threadId,
}: {
	threadId: string;
	orbId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const orb = useOrbOf(threadId);
	const template = useThread(threadId, (s) => s.thread?.portalUrlTemplate);
	const chosen = useUi((s) => s.portalPort[threadId]);
	if (!orb || orb.state === "deleted")
		return (
			<EmptyState icon="box" title={t("portal.orbGone")}>
				{t("portal.orbGoneHint")}
			</EmptyState>
		);
	if (!template)
		return (
			<EmptyState icon="globe" title={t("portal.disabledTitle")}>
				{t("portal.disabledHint")}
			</EmptyState>
		);
	const portals = orb.portals ?? [];
	const portal = portals.find((p) => p.port === chosen) ?? portals[0];
	if (!portal) return <EmptyState icon="globe" title={t("portal.none")} />;
	return (
		// 换线程即重建：工具条历史属于线程
		<PortalView
			key={threadId}
			threadId={threadId}
			orbId={orb.id}
			template={template}
			portals={portals}
			portal={portal}
		/>
	);
}
