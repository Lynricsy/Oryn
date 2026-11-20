/**
 * 信息面板「服务」入口行（计划 M4 5.3）：行右侧是各服务的状态标签（失败的排在前面），展开后是受管服务列表
 * （状态点、名称、端口、重启次数、失败原因）与行尾动作（启动 / 停止、重启、日志、在 Portal 中打开）。
 * orb 不是 `ready` 时动作禁用、状态点与标签置灰。orb 块里的故障提示行也从这里取失败的服务（`faultyServices`）。
 * 日志对话框打开期间每 3s 以 `bg=1` 重新拉取（不唤醒、不计用户交互）。对话框绑定打开它时的
 * {线程, orb, 服务名}：换线程 / 换 orb（路由复用本组件实例）或服务被移除时关闭，不会拿旧服务名去拉别的 orb 的日志。
 */
import { useQuery } from "@tanstack/react-query";
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
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
import { InfoRow, InfoTag, InfoTags, useExpandedKey } from "./info-parts";

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

/** 标签排序：失败在前，其次是启动中 / 等待重启，最后是正常与已停止。 */
const TONE_RANK: Partial<Record<Tone, number>> = { err: 0, warn: 1, ok: 2 };

/** 出故障（失败或未就绪）的服务；orb 不是 `ready` 时服务状态不作数，返回空。 */
export function faultyServices(orb: Orb): OrbService[] {
	if (orb.state !== "ready") return [];
	return (orb.services ?? []).filter((s) => STATUS_TONE[s.status] === "err");
}

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
					// 窄屏折行：手机上横向滚动看不到报错行的后半截（路径、模块名）
					className="max-h-[55dvh] overflow-auto rounded-lg border border-border bg-inset p-2.5 font-mono text-2xs leading-snug max-sm:whitespace-pre-wrap max-sm:[overflow-wrap:anywhere]"
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
					<span className="shrink-0 font-mono text-2xs text-faint">
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
					{/* 端口校验错误：服务根本没启动、没有日志，按来源给改端口的办法；其余原样显示并指向日志 */}
					{s.error === "port_conflict" || s.error === "forbidden_port" ? (
						<>
							{t(`services.errors.${s.error}`)}
							<span className="text-faint">
								{" "}
								{t(`services.errorFix.${s.source}`, { name: s.name })}
							</span>
						</>
					) : (
						<>
							{s.error}
							<span className="text-faint"> {t("services.errorHint")}</span>
						</>
					)}
				</p>
			)}
		</li>
	);
}

/** 入口行右侧的一个服务标签：正常运行的只给灰底 + 绿点 + 名称与端口，其余写出状态。 */
function ServiceTag({
	service: s,
	ready,
}: {
	service: OrbService;
	ready: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const status = t(`services.status.${s.status}`);
	const tone = ready ? STATUS_TONE[s.status] : "neutral";
	if (tone === "ok")
		return (
			<InfoTag title={status}>
				<span aria-hidden className="h-1.5 w-1.5 shrink-0 rounded-full bg-ok" />
				{s.name}
				{s.port !== undefined && (
					<span className="font-medium font-mono text-2xs">:{s.port}</span>
				)}
			</InfoTag>
		);
	return (
		<InfoTag tone={tone} title={s.error ?? status}>
			<span className="min-w-0 truncate">
				{s.name} {status}
				{tone === "err" &&
					s.restarts > 0 &&
					` · ${t("services.restarts", { count: s.restarts })}`}
			</span>
		</InfoTag>
	);
}

/** 日志对话框看的是哪个线程、哪台 orb 上的哪个服务。 */
interface LogTarget {
	threadId: string;
	orbId: string;
	name: string;
}

/** orb 没有受管服务时不渲染。 */
export function ServicesRow({
	threadId,
	orb,
}: {
	threadId: string;
	orb: Orb;
}): ReactNode {
	const { t } = useTranslation();
	const action = useServiceAction(orb.id);
	const { expanded, onToggle } = useExpandedKey("services:expanded");
	const [logOf, setLogOf] = useState<LogTarget | null>(null);
	const services = orb.services ?? [];
	const logName =
		logOf &&
		logOf.threadId === threadId &&
		logOf.orbId === orb.id &&
		services.some((s) => s.name === logOf.name)
			? logOf.name
			: null;
	// 身份变了或服务没了：真正清掉，回到原线程 / 服务重新出现时也不会自己再弹出来
	const stale = logOf !== null && logName === null;
	useEffect(() => {
		if (stale) setLogOf(null);
	}, [stale]);
	if (orb.state === "deleted" || services.length === 0) return null;
	const ready = orb.state === "ready";
	const portalPorts = new Set((orb.portals ?? []).map((p) => p.port));
	const rank = (s: OrbService) =>
		ready ? (TONE_RANK[STATUS_TONE[s.status]] ?? 3) : 3;
	const tags = services
		.toSorted((a, b) => rank(a) - rank(b))
		.map((s) => ({
			key: s.name,
			node: <ServiceTag service={s} ready={ready} />,
		}));
	return (
		<>
			<InfoRow
				icon="plugs"
				title={t("services.title")}
				testId="orb-services"
				expanded={expanded}
				onToggle={onToggle}
				status={
					<InfoTags
						meta={
							ready ? undefined : (
								<span title={t("services.notReady")}>
									{t(`orbState.${orb.state}`)}
								</span>
							)
						}
						tags={tags}
					/>
				}
			>
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
							onLog={() => setLogOf({ threadId, orbId: orb.id, name: s.name })}
						/>
					))}
				</ul>
				<div className="mt-1 empty:hidden">
					<ErrorText error={action.error} />
				</div>
			</InfoRow>
			{logName && (
				<ServiceLogDialog
					orbId={orb.id}
					name={logName}
					onClose={() => setLogOf(null)}
				/>
			)}
		</>
	);
}
