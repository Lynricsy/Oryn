/**
 * 信息面板 orb 卡里的「服务」小节（计划 M4 5.3）：受管服务列表（状态点、名称、端口、重启次数、失败原因）与行尾动作
 * （启动 / 停止、重启、日志、在 Portal 中打开）。orb 不是 `ready` 时动作禁用、状态点置灰。
 * 日志对话框打开期间每 3s 以 `bg=1` 重新拉取（不唤醒、不计用户交互）。
 */
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Orb, Schemas } from "../../api/client";
import { fetchServiceLog, qk, useServiceAction } from "../../api/queries";
import { uiStore } from "../../state/ui";
import { CopyButton } from "../ui/CopyButton";
import { StatusDot, type Tone } from "../ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Spinner,
} from "../ui/primitives";

type OrbService = Schemas["OrbService"];
type ServiceStatus = Schemas["OrbServiceStatus"];

/** 日志对话框的轮询间隔。 */
export const SERVICE_LOG_POLL_MS = 3_000;

const STATUS_TONE: Record<ServiceStatus, Tone> = {
	running: "ok",
	starting: "warn",
	backoff: "warn",
	unhealthy: "err",
	failed: "err",
	stopped: "neutral",
};

/** 在运行（或正要运行）的状态：行尾给「停止」，否则给「启动」。 */
const ACTIVE: Record<ServiceStatus, boolean> = {
	running: true,
	starting: true,
	unhealthy: true,
	backoff: true,
	stopped: false,
	failed: false,
};

export function ServiceLogDialog({
	orbId,
	name,
	onClose,
}: {
	orbId: string;
	name: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const log = useQuery({
		queryKey: qk.serviceLog(orbId, name),
		queryFn: ({ signal }) => fetchServiceLog(orbId, name, { bg: true, signal }),
		refetchInterval: SERVICE_LOG_POLL_MS,
		retry: false,
	});
	const pre = useRef<HTMLPreElement>(null);
	// 贴底跟随：在底部时新输出到来继续贴底；用户往上翻了就不动
	const stick = useRef(true);
	useLayoutEffect(() => {
		if (log.data !== undefined && pre.current && stick.current)
			pre.current.scrollTop = pre.current.scrollHeight;
	}, [log.data]);
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("services.logTitle", { name })}
			footer={<Button onClick={onClose}>{t("common.close")}</Button>}
		>
			<div className="mb-2 flex items-center gap-1.5 text-faint text-xs">
				<span>{t("services.logLive")}</span>
				<span className="flex-1" />
				{log.data && <CopyButton text={log.data} label={t("common.copy")} />}
				<IconButton
					icon="refresh"
					box="xs"
					size={13}
					label={t("services.logRefresh")}
					disabled={log.isFetching}
					onClick={() => void log.refetch()}
				/>
			</div>
			{log.isPending ? (
				<Spinner />
			) : log.data ? (
				<pre
					ref={pre}
					data-testid="service-log"
					onScroll={(e) => {
						const el = e.currentTarget;
						stick.current =
							el.scrollTop + el.clientHeight >= el.scrollHeight - 8;
					}}
					className="max-h-[55dvh] overflow-auto rounded-lg border border-border bg-inset p-2.5 font-mono text-2xs leading-snug"
				>
					{log.data}
				</pre>
			) : log.error ? (
				<ErrorText error={log.error} />
			) : (
				<p className="text-faint text-sm">{t("services.logEmpty")}</p>
			)}
			{/* 有旧日志时后台轮询失败（orb 冻结等）只补一行提示，不清掉已有内容 */}
			{log.data && log.error && (
				<div className="mt-2">
					<ErrorText error={log.error} />
				</div>
			)}
		</Dialog>
	);
}

function ServiceRow({
	threadId,
	service: s,
	ready,
	hasPortal,
	busy,
	onAction,
	onLog,
}: {
	threadId: string;
	service: OrbService;
	ready: boolean;
	hasPortal: boolean;
	busy: boolean;
	onAction: (action: "start" | "stop" | "restart") => void;
	onLog: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const status = t(`services.status.${s.status}`);
	const active = ACTIVE[s.status];
	const off = !ready || busy;
	return (
		<li data-service={s.name} data-status={s.status} className="text-xs">
			<div className="flex h-8 min-w-0 items-center gap-2">
				<span title={status} className="inline-flex">
					<StatusDot
						tone={ready ? STATUS_TONE[s.status] : "neutral"}
						label={status}
					/>
				</span>
				<span className="min-w-0 truncate font-medium text-fg">{s.name}</span>
				{s.port !== undefined && (
					<span className="shrink-0 rounded-sm border border-border bg-inset px-1.5 font-mono text-2xs text-muted leading-[18px]">
						:{s.port}
					</span>
				)}
				{s.status !== "running" && (
					<span className="shrink-0 text-2xs text-faint">{status}</span>
				)}
				{s.restarts > 0 && (
					<span className="min-w-0 truncate text-2xs text-faint tabular-nums">
						{t("services.restarts", { count: s.restarts })}
					</span>
				)}
				<span className="-mr-1.5 ml-auto flex shrink-0 items-center">
					{busy && <Spinner size={12} />}
					{active ? (
						<IconButton
							icon="stop"
							box="xs"
							size={12}
							label={t("services.stop", { name: s.name })}
							disabled={off}
							onClick={() => onAction("stop")}
						/>
					) : (
						<IconButton
							icon="play"
							box="xs"
							size={13}
							label={t("services.start", { name: s.name })}
							disabled={off}
							onClick={() => onAction("start")}
						/>
					)}
					<IconButton
						icon="restart"
						box="xs"
						size={13}
						label={t("services.restart", { name: s.name })}
						disabled={off}
						onClick={() => onAction("restart")}
					/>
					<IconButton
						icon="fileText"
						box="xs"
						size={13}
						label={t("services.log", { name: s.name })}
						disabled={!ready}
						onClick={onLog}
					/>
					{hasPortal && s.port !== undefined && (
						<IconButton
							icon="globe"
							box="xs"
							size={13}
							label={t("services.open", { name: s.name })}
							onClick={() => {
								const ui = uiStore.getState();
								ui.setPortalPort(threadId, s.port ?? 0);
								ui.setPanelTab("portal");
							}}
						/>
					)}
				</span>
			</div>
			{s.status === "failed" && s.error && (
				<p className="mb-1 ml-[15px] break-words text-2xs text-err">
					{/* 有专门文案的 orbd 错误码；其余原样显示 */}
					{s.error === "port_conflict" || s.error === "forbidden_port"
						? t(`services.errors.${s.error}`)
						: s.error}
					<span className="text-faint"> {t("services.errorHint")}</span>
				</p>
			)}
		</li>
	);
}

/** orb 没有受管服务时不渲染。 */
export function ServicesSection({
	threadId,
	orb,
}: {
	threadId: string;
	orb: Orb;
}): ReactNode {
	const { t } = useTranslation();
	const action = useServiceAction(orb.id);
	const [logOf, setLogOf] = useState<string | null>(null);
	const services = orb.services ?? [];
	if (orb.state === "deleted" || services.length === 0) return null;
	const ready = orb.state === "ready";
	const portalPorts = new Set((orb.portals ?? []).map((p) => p.port));
	return (
		<div className="mt-3" data-testid="orb-services">
			<h4 className="mb-1 flex items-center gap-1.5 font-medium text-2xs text-faint">
				{t("services.title")}
				{!ready && (
					<span className="font-normal" title={t("services.notReady")}>
						· {t(`orbState.${orb.state}`)}
					</span>
				)}
			</h4>
			<ul className="flex flex-col">
				{services.map((s) => (
					<ServiceRow
						key={s.name}
						threadId={threadId}
						service={s}
						ready={ready}
						hasPortal={s.port !== undefined && portalPorts.has(s.port)}
						busy={action.isPending && action.variables?.name === s.name}
						onAction={(a) => action.mutate({ name: s.name, action: a })}
						onLog={() => setLogOf(s.name)}
					/>
				))}
			</ul>
			<div className="mt-1 empty:hidden">
				<ErrorText error={action.error} />
			</div>
			{logOf && (
				<ServiceLogDialog
					orbId={orb.id}
					name={logOf}
					onClose={() => setLogOf(null)}
				/>
			)}
		</div>
	);
}
