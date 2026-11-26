/**
 * 信息面板的「沙箱」分组（M30）：这个线程的 agent 开出的临时沙箱。
 * 在用的在前（按创建时间），之后是最近 60 分钟内结束的（新的在上，置灰并给出原因）。每行是沙箱 id 末 6 位（悬停看全 id）、
 * 后端与网络标签，读数一行是状态、空闲回收倒计时（由 `idleDeadline` 每秒本地走表，过点显示「即将回收」）与 TTL 到期时刻；
 * 在用的行右侧有「销毁」（确认后调 REST，原因记为 `owner`）。一台都没有时整块不渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type Sandbox,
	useDestroySandbox,
	useThreadSandboxes,
} from "../../api/sandbox-queries";
import { confirmAction } from "../../lib/confirm";
import { uiStore, useUi } from "../../state/ui";
import { ListRow } from "../ui/data";
import { Icon } from "../ui/icons";
import { ErrorText, IconButton } from "../ui/primitives";
import { InfoGroup, InfoTag } from "./info-parts";
import { useTicker } from "./reports/parts";

/** 在用（含创建中、销毁中）还是已结束。 */
const LIVE: Record<Sandbox["state"], boolean> = {
	creating: true,
	ready: true,
	destroying: true,
	destroyed: false,
	failed: false,
};

const REASONS = {
	agent: true,
	owner: true,
	idle: true,
	ttl: true,
	max_lifetime: true,
	thread_archived: true,
	thread_deleted: true,
	owner_orb_gone: true,
	lost: true,
	create_failed: true,
	create_timeout: true,
} as const;
type Reason = keyof typeof REASONS;

function isReason(v: string | undefined): v is Reason {
	return v !== undefined && Object.hasOwn(REASONS, v);
}

/** 倒计时：不足 1 小时 `m:ss`，否则 `h:mm:ss`。 */
export function formatCountdown(ms: number): string {
	const total = Math.max(0, Math.ceil(ms / 1000));
	const h = Math.floor(total / 3600);
	const m = Math.floor((total % 3600) / 60);
	const s = total % 60;
	const ss = String(s).padStart(2, "0");
	return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

function Dot(): ReactNode {
	return (
		<span aria-hidden className="shrink-0">
			·
		</span>
	);
}

function SandboxRow({
	sb,
	now,
	onDestroy,
	pending,
}: {
	sb: Sandbox;
	now: number;
	onDestroy: (sb: Sandbox) => void;
	pending: boolean;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const live = LIVE[sb.state];
	const idleLeft = sb.idleDeadline - now;
	const clock = (ts: number) =>
		new Intl.DateTimeFormat(i18n.language, {
			hour: "2-digit",
			minute: "2-digit",
		}).format(ts);
	const reason = isReason(sb.destroyReason)
		? t(`infoPanel.sandboxes.reason.${sb.destroyReason}`)
		: sb.destroyReason;
	return (
		<li
			data-sandbox={sb.id}
			data-state={sb.state}
			className={live ? undefined : "opacity-60"}
		>
			<ListRow
				leading={
					<Icon
						name={sb.backend === "docker" ? "docker" : "cpu"}
						size={16}
						aria-hidden
						className="text-faint"
					/>
				}
				title={
					<>
						<span className="shrink-0 font-mono" title={sb.id}>
							{sb.id.slice(-6)}
						</span>
						<InfoTag>{t(`infoPanel.sandboxes.backend.${sb.backend}`)}</InfoTag>
						<InfoTag
							tone={sb.network === "egress" ? "accent" : "neutral"}
							title={t(`infoPanel.sandboxes.networkHint.${sb.network}`)}
						>
							{t(`infoPanel.sandboxes.network.${sb.network}`)}
						</InfoTag>
					</>
				}
				meta={
					<span className="flex min-w-0 items-center gap-1.5 tabular-nums">
						<span
							className={`shrink-0 ${sb.state === "failed" ? "text-err" : ""}`}
							title={sb.error}
						>
							{t(`infoPanel.sandboxes.state.${sb.state}`)}
						</span>
						{sb.state === "ready" && (
							<>
								<Dot />
								<span
									className={`shrink-0 ${idleLeft <= 0 ? "text-warn-ink" : ""}`}
									data-testid="sandbox-idle"
								>
									{idleLeft > 0
										? t("infoPanel.sandboxes.idleLeft", {
												time: formatCountdown(idleLeft),
											})
										: t("infoPanel.sandboxes.idleSoon")}
								</span>
							</>
						)}
						{live && sb.ttlDeadline !== undefined && (
							<>
								<Dot />
								<span className="shrink-0">
									{t("infoPanel.sandboxes.ttl", {
										time: clock(sb.ttlDeadline),
									})}
								</span>
							</>
						)}
						{!live && reason && (
							<>
								<Dot />
								<span className="min-w-0 truncate" title={sb.error}>
									{reason}
								</span>
							</>
						)}
					</span>
				}
				trailing={
					live && sb.state !== "destroying" ? (
						<IconButton
							icon="trash"
							box="sm"
							size={14}
							label={t("infoPanel.sandboxes.destroyLabel", {
								id: sb.id,
							})}
							disabled={pending}
							onClick={() => onDestroy(sb)}
						/>
					) : undefined
				}
			/>
		</li>
	);
}

export function SandboxSection({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const { data } = useThreadSandboxes(threadId);
	const destroy = useDestroySandbox();
	const collapsed = useUi((s) => !!s.collapsedCards.sandboxes);
	const all = data?.sandboxes ?? [];
	const live = all.filter((s) => LIVE[s.state]);
	const ended = all
		.filter((s) => !LIVE[s.state])
		.toSorted(
			(a, b) => (b.destroyedAt ?? b.createdAt) - (a.destroyedAt ?? a.createdAt),
		);
	const now = useTicker(live.length > 0 && !collapsed);
	if (all.length === 0) return null;
	const onDestroy = async (sb: Sandbox) => {
		const ok = await confirmAction({
			title: t("infoPanel.sandboxes.confirmTitle", { id: sb.id.slice(-6) }),
			body: t("infoPanel.sandboxes.confirmBody"),
			action: t("infoPanel.sandboxes.destroy"),
			danger: true,
		});
		if (ok) destroy.mutate(sb.id);
	};
	return (
		<InfoGroup
			title={t("infoPanel.sandboxes.title")}
			icon="package"
			collapsed={collapsed}
			onToggle={() => uiStore.getState().toggleCard("sandboxes")}
			right={
				live.length > 0
					? t("infoPanel.sandboxes.active", { n: live.length })
					: t("infoPanel.sandboxes.total", { n: all.length })
			}
		>
			{/* ListRow 自带 8px 内边距，外加 6px 与分组标题的 14px 对齐 */}
			<ul className="flex flex-col gap-px px-1.5">
				{[...live, ...ended].map((sb) => (
					<SandboxRow
						key={sb.id}
						sb={sb}
						now={now}
						onDestroy={(s) => void onDestroy(s)}
						pending={destroy.isPending && destroy.variables === sb.id}
					/>
				))}
			</ul>
			{destroy.error && (
				<div className="px-3.5 pt-1">
					<ErrorText error={destroy.error} />
				</div>
			)}
		</InfoGroup>
	);
}
