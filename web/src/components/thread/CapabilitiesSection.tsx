/**
 * 信息面板「能力」入口行（只读）：当前会话加载的技能、MCP 服务器、子 agent 定义、钩子与配置警告。
 * 默认收起，展开后才向 agent 查询（五条 `get_*`，60 秒内复用缓存；收起后缓存仍在时行右侧照样给出摘要与警告数）；
 * 不提供启停、增删、授权等操作——orb 内 omp 的配置以 omp-home 下发为准，在这里改会在下次下发时被覆盖。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ResponseData } from "../../omp/types";
import { realtime } from "../../realtime/socket";
import { agentQueryable, useThread } from "../../state/stores";
import { Badge, Callout, ListRow, StatusDot, type Tone } from "../ui/data";
import { ErrorText, IconButton } from "../ui/primitives";
import { InfoRow, InfoTag, InfoTags, useExpandedKey } from "./info-parts";

type CapabilityCommand =
	| "get_skills"
	| "get_mcp_servers"
	| "get_agent_definitions"
	| "get_hooks"
	| "get_config_warnings";

function useCapability<C extends CapabilityCommand>(
	threadId: string,
	command: C,
	enabled: boolean,
) {
	return useQuery({
		queryKey: ["rpc", threadId, "capabilities", command],
		enabled,
		staleTime: 60_000,
		retry: false,
		queryFn: () =>
			realtime.cmd(threadId, { type: command }) as Promise<ResponseData<C>>,
	});
}

const MCP_TONE: Record<string, Tone> = {
	connected: "ok",
	connecting: "accent",
	disconnected: "neutral",
};

function Group({
	title,
	count,
	children,
}: {
	title: string;
	count: number;
	children: ReactNode;
}) {
	return (
		<div className="-mx-1.5 mb-1.5 last:mb-0">
			<div className="mb-0.5 flex items-center gap-1.5 px-1.5 font-medium text-2xs text-faint">
				{title}
				<span className="tabular-nums">{count}</span>
			</div>
			<ul className="flex flex-col gap-px">{children}</ul>
		</div>
	);
}

/** 一行：名字、单行截断的说明、右侧徽标；`off` 时整行置灰。 */
function Row({
	name,
	description,
	leading,
	badges,
	off,
}: {
	name: string;
	description?: ReactNode;
	leading?: ReactNode;
	badges?: ReactNode;
	off?: boolean;
}) {
	return (
		<li className={off ? "opacity-50" : undefined}>
			<ListRow
				leading={leading}
				title={<span className="truncate font-medium">{name}</span>}
				meta={
					description ? (
						<span className="min-w-0 flex-1 truncate">{description}</span>
					) : undefined
				}
				trailing={badges}
			/>
		</li>
	);
}

export function CapabilitiesRow({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const work = useThread(
		threadId,
		(s) => (s.thread?.kind ?? "work") === "work",
	);
	const queryable = useThread(threadId, agentQueryable);
	const { expanded, onToggle } = useExpandedKey("capabilities:expanded");
	const on = work && queryable && expanded;
	const skills = useCapability(threadId, "get_skills", on);
	const mcp = useCapability(threadId, "get_mcp_servers", on);
	const agents = useCapability(threadId, "get_agent_definitions", on);
	const hooks = useCapability(threadId, "get_hooks", on);
	const warnings = useCapability(threadId, "get_config_warnings", on);
	if (!work || !queryable) return null;

	const skillList = skills.data?.skills ?? [];
	const servers = mcp.data?.servers ?? [];
	const agentList = agents.data?.agents ?? [];
	const hookList = hooks.data?.hooks ?? [];
	const warningList = warnings.data?.warnings ?? [];
	const queries = [skills, mcp, agents, hooks, warnings];
	const settled = queries.every((q) => q.data !== undefined || q.error);
	const empty =
		skillList.length +
			servers.length +
			agentList.length +
			hookList.length +
			warningList.length ===
		0;
	const error = queries.find((q) => q.error)?.error;

	return (
		<InfoRow
			icon="sparkle"
			title={t("panel.capabilities")}
			testId="info-capabilities"
			expanded={expanded}
			onToggle={onToggle}
			status={
				<InfoTags
					meta={
						skills.data &&
						mcp.data &&
						t("capabilities.summary", {
							skills: skillList.length,
							connected: servers.filter((s) => s.status === "connected").length,
							total: servers.length,
						})
					}
					tags={
						warningList.length > 0
							? [
									{
										key: "warnings",
										node: (
											<InfoTag tone="warn">
												{t("capabilities.warningCount", {
													count: warningList.length,
												})}
											</InfoTag>
										),
									},
								]
							: []
					}
				/>
			}
			action={
				<IconButton
					icon="refresh"
					size={13}
					box="xs"
					label={t("capabilities.refresh")}
					onClick={() =>
						void queryClient.invalidateQueries({
							queryKey: ["rpc", threadId, "capabilities"],
						})
					}
				/>
			}
		>
			{warningList.length > 0 && (
				<div className="mb-2 flex flex-col gap-1">
					{warningList.map((w) => (
						<Callout key={w} tone="warn">
							{w}
						</Callout>
					))}
				</div>
			)}
			{skillList.length > 0 && (
				<Group title={t("capabilities.skills")} count={skillList.length}>
					{skillList.map((s) => (
						<Row
							key={`${s.source}:${s.name}`}
							name={s.name}
							description={s.description}
							badges={<Badge>{s.source}</Badge>}
							off={s.enabled === false}
						/>
					))}
				</Group>
			)}
			{servers.length > 0 && (
				<Group title={t("capabilities.mcp")} count={servers.length}>
					{servers.map((s) => (
						<Row
							key={s.name}
							name={s.name}
							leading={
								<StatusDot
									tone={MCP_TONE[s.status] ?? "neutral"}
									label={s.status}
								/>
							}
							description={
								<span className="tabular-nums">
									{s.toolCount} · {s.transport}
								</span>
							}
							badges={
								(s.authState === "expired" || s.authState === "required") && (
									<Badge tone="warn">{t("capabilities.authRequired")}</Badge>
								)
							}
						/>
					))}
				</Group>
			)}
			{agentList.length > 0 && (
				<Group title={t("capabilities.agents")} count={agentList.length}>
					{agentList.map((a) => (
						<Row
							key={`${a.source}:${a.name}`}
							name={a.name}
							description={a.description}
							badges={<Badge>{a.source}</Badge>}
						/>
					))}
				</Group>
			)}
			{hookList.length > 0 && (
				<Group title={t("capabilities.hooks")} count={hookList.length}>
					{hookList.map((h) => (
						<Row
							key={h.id}
							name={h.name}
							description={h.event}
							off={h.enabled === false}
						/>
					))}
				</Group>
			)}
			{/* 部分命令失败（如旧 Core 不认）时其余小节照常显示；全空时才给出错误 */}
			{empty && <ErrorText error={error} />}
			{settled && empty && !error && (
				<p className="text-faint text-xs">{t("capabilities.empty")}</p>
			)}
		</InfoRow>
	);
}
