/**
 * 设置 → 全局助手（M19 步骤 1.6 / 4.4，M26）：四张卡片。
 * 1. 联网：助手沙箱经 orynd 代理访问公网的总开关（关掉后 `web_search` 与读网页失败）。
 * 2. MCP 服务器：全局 `mcp.json` 里的服务器——名字、主机、传输方式、不可用的原因，可用的逐个开关；可用且开着的
 *    行尾有「研究可用工具（N）」，打开 `McpResearchToolsDialog` 勾选后台研究可调用的工具（M26）。
 * 3. 后台研究（M26，`ResearchCard`）：总开关、四条管线、研究员的模型、每日上限与研究偏好，整体 PUT 研究设置。
 * 4. 助手的电脑：创建它时用的 provider 与 orb 类型（都留空走 orb 的默认解析）；只影响下一次创建。
 * 前两张与最后一张整体 PUT 助手设置；请求进行中先显示要改成的值。改动后助手对话在静止时用新配置重启。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type AssistantMcpServer,
	type AssistantSettings as Settings,
	useAssistantMcpServers,
	useAssistantSettings,
	usePutAssistantSettings,
} from "../../api/assistant-queries";
import { useProviders } from "../../api/queries";
import { useResearchSettings } from "../../api/research-queries";
import { McpResearchToolsDialog } from "../../components/assistant/McpResearchToolsDialog";
import { Badge, Card, ListRow } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { typeHasDocker } from "../../components/ui/OrbBadges";
import {
	Button,
	ErrorText,
	Field,
	Select,
	Spinner,
	Toggle,
} from "../../components/ui/primitives";
import { ResearchCard } from "./ResearchCard";

function ServerRow({
	server,
	enabled,
	disabled,
	researchTools,
	onToggle,
	onResearchTools,
}: {
	server: AssistantMcpServer;
	enabled: boolean;
	disabled: boolean;
	/** 已勾选的研究可用工具数 */
	researchTools: number;
	onToggle: (on: boolean) => void;
	onResearchTools: () => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<li data-mcp-server={server.name}>
			<ListRow
				leading={
					<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
						<Icon name={server.usable ? "link" : "alert"} size={15} />
					</span>
				}
				title={<span className="truncate font-medium">{server.name}</span>}
				meta={
					<>
						<Badge>
							{t(`assistantSettings.transport.${server.transport}`)}
						</Badge>
						{server.host && <span className="font-mono">{server.host}</span>}
						{!server.usable && server.reason && (
							<span className="text-warn">
								{t(`assistantSettings.reason.${server.reason}`)}
							</span>
						)}
					</>
				}
				trailing={
					server.usable ? (
						<span className="flex shrink-0 items-center gap-2">
							{enabled && (
								<Button size="sm" variant="ghost" onClick={onResearchTools}>
									{t("settings.research.mcpTools.button", {
										n: researchTools,
									})}
								</Button>
							)}
							<Toggle
								checked={enabled}
								disabled={disabled}
								label={
									<span className="sr-only">
										{t("assistantSettings.mcpToggle", { name: server.name })}
									</span>
								}
								onChange={onToggle}
							/>
						</span>
					) : (
						<Badge tone="warn">{t("assistantSettings.unusable")}</Badge>
					)
				}
			/>
		</li>
	);
}

export default function AssistantSettings(): ReactNode {
	const { t } = useTranslation();
	const settings = useAssistantSettings();
	const servers = useAssistantMcpServers();
	const put = usePutAssistantSettings();
	const providers = useProviders();
	const research = useResearchSettings();
	// 正在编辑研究可用工具的服务器
	const [toolsFor, setToolsFor] = useState<string | null>(null);
	// 请求进行中先显示要改成的值
	const current: Settings | undefined = put.isPending
		? put.variables
		: settings.data;
	const save = (patch: Partial<Settings>) => {
		if (current) put.mutate({ ...current, ...patch });
	};
	const setServer = (name: string, on: boolean) => {
		if (!current) return;
		const rest = current.disabledMcpServers.filter((n) => n !== name);
		save({ disabledMcpServers: on ? rest : [...rest, name] });
	};
	const computer = current?.computer ?? {};
	const provider = providers.data?.providers.find(
		(p) => p.name === computer.provider,
	);
	return (
		<>
			<Card title={t("assistantSettings.webTitle")} icon="globe">
				{settings.isPending ? (
					<Spinner />
				) : settings.isError ? (
					<ErrorText error={settings.error} />
				) : (
					<div className="flex flex-col gap-2">
						<Toggle
							checked={current?.webAccess ?? false}
							disabled={put.isPending}
							label={t("assistantSettings.webAccess")}
							onChange={(webAccess) => save({ webAccess })}
						/>
						<p className="text-faint text-xs">
							{t("assistantSettings.webHint")}
						</p>
					</div>
				)}
				<ErrorText error={put.error} />
			</Card>
			<Card title={t("assistantSettings.mcpTitle")} icon="wrench">
				<p className="text-faint text-xs">{t("assistantSettings.mcpHint")}</p>
				{servers.isPending || settings.isPending ? (
					<Spinner />
				) : servers.isError ? (
					<ErrorText error={servers.error} />
				) : servers.data.servers.length === 0 ? (
					<p className="text-faint text-sm">
						{t("assistantSettings.mcpEmpty")}
					</p>
				) : (
					<ul className="-mx-2 flex flex-col">
						{servers.data.servers.map((s) => (
							<ServerRow
								key={s.name}
								server={s}
								enabled={
									current
										? !current.disabledMcpServers.includes(s.name)
										: s.enabled
								}
								disabled={put.isPending || !current}
								researchTools={
									research.data?.settings.mcpTools[s.name]?.length ?? 0
								}
								onToggle={(on) => setServer(s.name, on)}
								onResearchTools={() => setToolsFor(s.name)}
							/>
						))}
					</ul>
				)}
				{toolsFor && (
					<McpResearchToolsDialog
						server={toolsFor}
						onClose={() => setToolsFor(null)}
					/>
				)}
			</Card>
			<ResearchCard />
			<Card title={t("assistantSettings.computerTitle")} icon="laptop">
				<p className="text-faint text-xs">
					{t("assistantSettings.computerHint")}
				</p>
				{settings.isPending ? (
					<Spinner />
				) : (
					<div className="mt-2 grid gap-3 sm:grid-cols-2">
						<Field label={t("assistantSettings.computerProvider")}>
							<Select
								data-testid="computer-provider"
								value={computer.provider ?? ""}
								disabled={put.isPending || !current}
								onChange={(e) =>
									// 换 provider 时类型回到默认（各 provider 的类型表不同）
									save({
										computer: e.target.value
											? { provider: e.target.value }
											: {},
									})
								}
							>
								<option value="">
									{t("assistantSettings.computerDefault")}
								</option>
								{providers.data?.providers.map((p) => (
									<option key={p.id} value={p.name}>
										{p.name}
									</option>
								))}
							</Select>
						</Field>
						<Field label={t("assistantSettings.computerOrbType")}>
							<Select
								data-testid="computer-orb-type"
								value={computer.orbType ?? ""}
								disabled={put.isPending || !current}
								onChange={(e) =>
									save({
										computer: {
											...computer,
											orbType: e.target.value || undefined,
										},
									})
								}
							>
								<option value="">
									{t("assistantSettings.computerDefault")}
								</option>
								{(provider?.orbTypes ?? []).map((ot) => (
									<option key={ot.name} value={ot.name}>
										{typeHasDocker(ot)
											? `${ot.name} · ${t("accel.docker")}`
											: ot.name}
									</option>
								))}
							</Select>
						</Field>
					</div>
				)}
			</Card>
		</>
	);
}
