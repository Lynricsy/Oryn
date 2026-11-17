/**
 * 设置 → 全局助手（M19 步骤 1.6）：两张卡片。
 * 1. 联网：助手沙箱经 orynd 代理访问公网的总开关（关掉后 `web_search` 与读网页失败）。
 * 2. MCP 服务器：全局 `mcp.json` 里的服务器——名字、主机、传输方式、不可用的原因，可用的逐个开关。
 * 两处都整体 PUT 设置；请求进行中先显示要改成的值。改动后助手对话在静止时用新配置重启。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type AssistantMcpServer,
	type AssistantSettings as Settings,
	useAssistantMcpServers,
	useAssistantSettings,
	usePutAssistantSettings,
} from "../../api/assistant-queries";
import { Badge, Card, ListRow } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import { ErrorText, Spinner, Toggle } from "../../components/ui/primitives";

function ServerRow({
	server,
	enabled,
	disabled,
	onToggle,
}: {
	server: AssistantMcpServer;
	enabled: boolean;
	disabled: boolean;
	onToggle: (on: boolean) => void;
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
								onToggle={(on) => setServer(s.name, on)}
							/>
						))}
					</ul>
				)}
			</Card>
		</>
	);
}
