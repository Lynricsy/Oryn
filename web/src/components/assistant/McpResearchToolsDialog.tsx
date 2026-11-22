/**
 * 设置 → 全局助手 → MCP 服务器行的「研究可用工具」（M26）：勾选后台研究可以调用的该服务器工具。
 * orynd 现连服务器列出工具：每个一个复选框，服务器自报只读的旁标「自报只读」；不预先勾选任何一项（已保存在白名单里的
 * 显示为已勾，服务器这次没列出的已保存项也列出来，可以取消）。列不出来（SSE 服务器或上游失败）时改为每行一个工具名的
 * 输入框。保存时把该服务器的名单并入 `mcpTools`（名单为空则去掉该服务器）后整体 PUT 研究设置，成功即关闭。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../api/client";
import {
	MCP_TOOL_NAME_MAX,
	MCP_TOOLS_MAX,
	useAssistantMcpServerTools,
	usePutResearchSettings,
	useResearchSettings,
} from "../../api/research-queries";
import { Badge, Callout } from "../ui/data";
import {
	Button,
	Checkbox,
	Dialog,
	describeError,
	ErrorText,
	Field,
	Spinner,
	TextArea,
} from "../ui/primitives";

export function McpResearchToolsDialog({
	server,
	onClose,
}: {
	server: string;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const envelope = useResearchSettings();
	const tools = useAssistantMcpServerTools(server);
	const put = usePutResearchSettings();
	const current = envelope.data?.settings;
	const saved = current?.mcpTools[server] ?? [];
	// null = 没改过，显示已保存的名单
	const [picked, setPicked] = useState<string[] | null>(null);
	const [manual, setManual] = useState<string | null>(null);
	const selected = picked ?? saved;
	const listed = tools.data?.tools ?? [];
	const unlisted = saved.filter((n) => !listed.some((x) => x.name === n));
	const manualMode = tools.isError;
	const manualText = manual ?? saved.join("\n");
	// 手动输入：每行一个工具名，去掉首尾空白、空行与重复
	const names = manualMode
		? [
				...new Set(
					manualText
						.split("\n")
						.map((s) => s.trim())
						.filter(Boolean),
				),
			]
		: [...listed.map((x) => x.name), ...unlisted].filter((n) =>
				selected.includes(n),
			);
	const tooMany = names.length > MCP_TOOLS_MAX;
	const tooLong = names.some((n) => [...n].length > MCP_TOOL_NAME_MAX);
	const save = () => {
		if (!current) return;
		const { [server]: _drop, ...others } = current.mcpTools;
		put.mutate(
			{
				...current,
				mcpTools: names.length ? { ...others, [server]: names } : others,
			},
			{ onSuccess: onClose },
		);
	};
	const sse =
		tools.error instanceof ApiError &&
		tools.error.code === "unsupported_transport";
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("settings.research.mcpTools.title", { name: server })}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={
							!current || tools.isPending || put.isPending || tooMany || tooLong
						}
						onClick={save}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<div data-testid="mcp-research-tools" className="flex flex-col gap-3">
				<Callout tone="warn">{t("settings.research.mcpTools.note")}</Callout>
				{tools.isPending || envelope.isPending ? (
					<Spinner size={14} />
				) : envelope.isError ? (
					<ErrorText error={envelope.error} />
				) : manualMode ? (
					<>
						<p className="text-muted text-xs">
							{sse
								? t("settings.research.mcpTools.sse")
								: t("settings.research.mcpTools.failed", {
										error: describeError(tools.error, t),
									})}
						</p>
						<Field label={t("settings.research.mcpTools.manualLabel")}>
							<TextArea
								rows={6}
								value={manualText}
								onChange={(e) => setManual(e.target.value)}
							/>
						</Field>
					</>
				) : listed.length === 0 && unlisted.length === 0 ? (
					<p className="text-faint text-sm">
						{t("settings.research.mcpTools.empty")}
					</p>
				) : (
					<ul className="flex flex-col gap-1">
						{[
							...listed,
							...unlisted.map((name) => ({ name, missing: true as const })),
						].map((tool) => (
							<li key={tool.name} data-mcp-tool={tool.name}>
								{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
								<label className="flex cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 hover:bg-hover">
									<Checkbox
										className="mt-0.5"
										checked={selected.includes(tool.name)}
										onChange={(e) =>
											setPicked(
												e.target.checked
													? [...selected, tool.name]
													: selected.filter((n) => n !== tool.name),
											)
										}
									/>
									<span className="flex min-w-0 flex-1 flex-col gap-0.5">
										<span className="flex min-w-0 flex-wrap items-center gap-1.5 text-sm">
											<span className="truncate font-mono">{tool.name}</span>
											{"readOnly" in tool && tool.readOnly && (
												<Badge tone="ok">
													{t("settings.research.mcpTools.readOnly")}
												</Badge>
											)}
										</span>
										{"missing" in tool ? (
											<span className="text-faint text-xs">
												{t("settings.research.mcpTools.unlisted")}
											</span>
										) : (
											tool.description && (
												<span className="break-words text-faint text-xs">
													{tool.description}
												</span>
											)
										)}
									</span>
								</label>
							</li>
						))}
					</ul>
				)}
				{tooMany && (
					<p role="alert" className="text-err text-xs">
						{t("settings.research.mcpTools.tooMany", { max: MCP_TOOLS_MAX })}
					</p>
				)}
				{tooLong && (
					<p role="alert" className="text-err text-xs">
						{t("settings.research.mcpTools.tooLong", {
							max: MCP_TOOL_NAME_MAX,
						})}
					</p>
				)}
				<ErrorText error={put.error} />
			</div>
		</Dialog>
	);
}
