/**
 * 设置 → 全局助手（M19 步骤 1.6 / 4.4，M26）：助手相关的设置都在这一页，按工作台原生（D）分组：
 * 1. 全局助手：名字与头像（`AssistantIdentityCard`，带底栏预览）。
 * 2. 模型与思考档位：全局助手默认使用的模型与思考档位（留空跟随系统默认）；改动同时切换主线，已有其他对话不变。
 * 3. 自定义提示词：附加到全局助手系统提示词中的指令（最多 8000 字，不影响内置工具与安全规则）；改动后未归档助手对话在空闲后自动应用。
 * 4. 联网与 MCP：助手沙箱经 orynd 代理访问公网的总开关（关掉后 `web_search` 与读网页失败）；全局 `mcp.json` 里的服务器——
 *    名字、主机、传输方式、不可用的原因，可用的逐个开关；可用且开着的行尾有「研究可用工具（N）」，打开
 *    `McpResearchToolsDialog` 勾选后台研究可调用的工具（M26）。
 * 5. 后台研究（M26，`ResearchCard`）：总开关、四条管线、研究员的模型、每日上限与研究偏好，整体 PUT 研究设置。
 * 6. 助手的电脑：创建它时用的 provider 与 orb 类型（都留空走 orb 的默认解析）；只影响下一次创建。
 * 7. 助手记忆（M17，`OwnerMemorySection`）。
 * 2–4、6 共用一份助手设置：加载与加载失败在页内统一处理一次；都整体 PUT，请求进行中先显示要改成的值；
 * 保存失败的错误显示在发起改动的那一组下方。
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
import { ModelPickerBody } from "../../components/composer/ModelPickerBody";
import { GroupNote } from "../../components/settings/agent-config-ui";
import { Icon } from "../../components/ui/icons";
import { ModelLogo } from "../../components/ui/ModelLogo";
import { typeHasDocker } from "../../components/ui/OrbBadges";
import { Popover } from "../../components/ui/PickerPopover";
import {
	Button,
	ErrorText,
	Menu,
	type MenuItem,
	Select,
	Spinner,
	TextArea,
	Toggle,
} from "../../components/ui/primitives";
import { type DIAL_LEVELS, LevelGlyph } from "../../components/ui/ThinkingDial";
import {
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
	StatusText,
} from "../../components/ui/workbench";
import { THINKING_LEVELS } from "../../lib/omp-config";
import { useModelCaps } from "../../state/model-caps";
import { OwnerMemorySection } from "./agent/memory/OwnerMemorySection";
import { AssistantIdentityCard } from "./IdentitySettings";
import { ResearchCard } from "./ResearchCard";

/** 自定义附加系统提示词的字数上限（同服务端）。 */
const SYSTEM_PROMPT_LIMIT = 8000;

/** 发起保存的分组：保存失败的错误显示在它下方。 */
type Area = "model" | "prompt" | "web" | "computer";

/** 行说明里的分隔点 */
const SEP = (
	<span aria-hidden className="text-faint">
		·
	</span>
);

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
		<GroupRow
			icon={server.usable ? "link" : "alert"}
			// 行的定位钩子挂在标题上（GroupRow 本身是 li，不再包一层）
			title={
				<span data-mcp-server={server.name} className="truncate">
					{server.name}
				</span>
			}
			meta={
				<>
					<span>{t(`assistantSettings.transport.${server.transport}`)}</span>
					{server.host && (
						<>
							{SEP}
							<span className="font-mono">{server.host}</span>
						</>
					)}
					{!server.usable && server.reason && (
						<>
							{SEP}
							<span>{t(`assistantSettings.reason.${server.reason}`)}</span>
						</>
					)}
				</>
			}
			trailing={
				server.usable ? (
					<>
						{enabled && (
							<Button size="sm" variant="ghost" onClick={onResearchTools}>
								{t("settings.research.mcpTools.button", {
									n: researchTools,
								})}
							</Button>
						)}
						<Toggle
							hideLabel
							checked={enabled}
							disabled={disabled}
							label={t("assistantSettings.mcpToggle", { name: server.name })}
							onChange={onToggle}
						/>
					</>
				) : (
					<StatusText tone="warn">{t("assistantSettings.unusable")}</StatusText>
				)
			}
		/>
	);
}

export default function AssistantSettings(): ReactNode {
	const settings = useAssistantSettings();
	return (
		<>
			<AssistantIdentityCard />
			{settings.data ? (
				<SettingsGroups saved={settings.data} />
			) : (
				<>
					{settings.isError ? (
						<ErrorText error={settings.error} />
					) : (
						<Spinner />
					)}
					<ResearchCard />
				</>
			)}
			<OwnerMemorySection />
		</>
	);
}

/** 共用助手设置的各组：模型与思考、提示词、联网与 MCP，之后是后台研究（自有设置），最后助手的电脑。 */
function SettingsGroups({ saved }: { saved: Settings }): ReactNode {
	const { t } = useTranslation();
	const servers = useAssistantMcpServers();
	const put = usePutAssistantSettings();
	const providers = useProviders();
	const research = useResearchSettings();
	// 正在编辑研究可用工具的服务器
	const [toolsFor, setToolsFor] = useState<string | null>(null);
	// 正在弹出全局助手模型选择器
	const [pickingModel, setPickingModel] = useState(false);
	// 自定义提示词草稿（null = 未编辑，显示服务端值）
	const [promptDraft, setPromptDraft] = useState<string | null>(null);
	// 最近一次保存由哪一组发起
	const [area, setArea] = useState<Area>("model");
	// 请求进行中先显示要改成的值
	const current: Settings = (put.isPending && put.variables) || saved;
	const save = (at: Area, patch: Partial<Settings>, onSuccess?: () => void) => {
		const next: Settings = { ...current, ...patch };
		if (!next.model) delete next.model;
		if (!next.thinkingLevel) delete next.thinkingLevel;
		if (!next.systemPrompt) delete next.systemPrompt;
		setArea(at);
		put.mutate(next, onSuccess ? { onSuccess } : undefined);
	};
	const putError = (at: Area) =>
		area === at ? <ErrorText error={put.error} /> : null;
	const setServer = (name: string, on: boolean) => {
		const rest = current.disabledMcpServers.filter((n) => n !== name);
		save("web", { disabledMcpServers: on ? rest : [...rest, name] });
	};
	const model = current.model;
	const thinking = current.thinkingLevel ?? "";
	const modelCaps = useModelCaps(model ? `${model.provider}/${model.id}` : "");
	const thinkingUnsupported =
		model !== undefined &&
		modelCaps !== undefined &&
		modelCaps.thinking.length === 0;
	const thinkingOptions: readonly string[] = (() => {
		if (thinkingUnsupported) return thinking ? ["", thinking] : [""];
		const base: readonly string[] =
			model && modelCaps ? ["auto", ...modelCaps.thinking] : THINKING_LEVELS;
		return thinking && !base.includes(thinking)
			? ["", thinking, ...base]
			: ["", ...base];
	})();
	const thinkingLabel = (l: string) =>
		l === ""
			? t("assistantSettings.thinkingDefault")
			: t(`model.levels.${l as (typeof DIAL_LEVELS)[number] | "auto" | "max"}`);
	const thinkingItems: MenuItem[] = thinkingOptions.map((l) => ({
		key: l || "default",
		label: thinkingLabel(l),
		leading: <LevelGlyph level={l} size={14} />,
		checked: l === thinking,
		onSelect: () => save("model", { thinkingLevel: l || undefined }),
	}));
	const computer = current.computer ?? {};
	const provider = providers.data?.providers.find(
		(p) => p.name === computer.provider,
	);
	const busy = put.isPending;
	const savedPrompt = current.systemPrompt ?? "";
	const promptText = promptDraft ?? savedPrompt;
	const promptCount = [...promptText].length;
	const promptOver = promptCount > SYSTEM_PROMPT_LIMIT;
	const promptDirty =
		promptDraft !== null && promptDraft.trim() !== savedPrompt;
	return (
		<>
			<Group title={t("assistantSettings.modelTitle")}>
				<div data-testid="assistant-model-card" className="contents">
					<GroupList>
						<GroupRow
							icon="cpu"
							title={t("assistantSettings.model")}
							meta={
								model ? (
									<span
										data-testid="assistant-model"
										className="inline-flex min-w-0 items-center gap-1.5"
									>
										<ModelLogo
											model={model.id}
											provider={model.provider}
											size={12}
										/>
										<span className="truncate font-mono">{`${model.provider}/${model.id}`}</span>
									</span>
								) : (
									<span data-testid="assistant-model-default">
										{t("assistantSettings.modelDefault")}
									</span>
								)
							}
							trailing={
								<>
									{model && (
										<Button
											size="sm"
											variant="ghost"
											disabled={busy}
											onClick={() => save("model", { model: undefined })}
										>
											{t("assistantSettings.modelReset")}
										</Button>
									)}
									<span className="relative inline-flex">
										<Button
											size="sm"
											aria-expanded={pickingModel}
											disabled={busy}
											onClick={() => setPickingModel((v) => !v)}
										>
											{t("assistantSettings.modelChange")}
										</Button>
										{pickingModel && (
											<Popover
												side="bottom"
												onClose={() => setPickingModel(false)}
											>
												<div className="p-1">
													<ModelPickerBody
														free
														current={model}
														onPick={({ provider: p, id }) => {
															save("model", { model: { provider: p, id } });
															setPickingModel(false);
														}}
													/>
												</div>
											</Popover>
										)}
									</span>
								</>
							}
						/>
						<GroupRow
							icon="brain"
							title={t("assistantSettings.thinking")}
							meta={
								thinkingUnsupported
									? t("assistantSettings.thinkingUnsupported")
									: undefined
							}
							trailing={
								<>
									{thinking && (
										<Button
											size="sm"
											variant="ghost"
											disabled={busy}
											onClick={() =>
												save("model", { thinkingLevel: undefined })
											}
										>
											{t("assistantSettings.modelReset")}
										</Button>
									)}
									{!(thinkingUnsupported && !thinking) && (
										<Menu
											side="bottom"
											align="right"
											items={thinkingItems}
											className="inline-flex"
											trigger={(p) => (
												<Button
													size="sm"
													data-testid="assistant-thinking"
													disabled={busy}
													aria-label={t("model.thinkingButton", {
														level: thinkingLabel(thinking),
													})}
													{...p}
												>
													<LevelGlyph level={thinking} size={13} />
													<span>{thinkingLabel(thinking)}</span>
													<Icon
														name="chevronDown"
														size={12}
														className="text-muted"
													/>
												</Button>
											)}
										/>
									)}
								</>
							}
						/>
					</GroupList>
				</div>
				<GroupNote>{t("assistantSettings.modelHint")}</GroupNote>
				{putError("model")}
			</Group>
			<Group title={t("assistantSettings.promptTitle")}>
				<GroupPanel>
					<div
						data-testid="assistant-prompt-card"
						className="flex flex-col gap-2"
					>
						<TextArea
							mono={false}
							rows={5}
							aria-label={t("assistantSettings.promptTitle")}
							aria-invalid={promptOver || undefined}
							placeholder={t("assistantSettings.promptPlaceholder")}
							disabled={busy}
							value={promptText}
							onChange={(e) => setPromptDraft(e.target.value)}
						/>
						<div className="flex flex-wrap items-center justify-between gap-3 text-xs">
							<span
								role={promptOver ? "alert" : undefined}
								className="text-err"
							>
								{promptOver
									? t("assistantSettings.promptTooLong", {
											limit: SYSTEM_PROMPT_LIMIT,
										})
									: ""}
							</span>
							<span className="ml-auto flex shrink-0 items-center gap-2">
								<span
									data-testid="assistant-prompt-count"
									className={`tabular-nums ${promptOver ? "text-err" : "text-faint"}`}
								>
									{promptCount} / {SYSTEM_PROMPT_LIMIT}
								</span>
								{savedPrompt && (
									<Button
										size="sm"
										variant="ghost"
										disabled={busy}
										onClick={() =>
											save("prompt", { systemPrompt: undefined }, () =>
												setPromptDraft(null),
											)
										}
									>
										{t("assistantSettings.promptClear")}
									</Button>
								)}
								<Button
									size="sm"
									variant="primary"
									disabled={!promptDirty || promptOver || busy}
									onClick={() =>
										save(
											"prompt",
											{ systemPrompt: promptText.trim() || undefined },
											() => setPromptDraft(null),
										)
									}
								>
									{t("common.save")}
								</Button>
							</span>
						</div>
					</div>
				</GroupPanel>
				<GroupNote>{t("assistantSettings.promptHint")}</GroupNote>
				{putError("prompt")}
			</Group>
			<Group title={t("assistantSettings.webMcpTitle")}>
				<GroupList>
					<GroupRow
						icon="globe"
						title={t("assistantSettings.webAccess")}
						meta={t("assistantSettings.webHint")}
						trailing={
							<Toggle
								hideLabel
								checked={current.webAccess}
								disabled={busy}
								label={t("assistantSettings.webAccess")}
								onChange={(webAccess) => save("web", { webAccess })}
							/>
						}
					/>
					{servers.isPending ? (
						<GroupRow
							icon="wrench"
							title={t("assistantSettings.mcpTitle")}
							trailing={<Spinner />}
						/>
					) : servers.isError ? (
						<GroupRow icon="wrench" title={t("assistantSettings.mcpTitle")}>
							<ErrorText error={servers.error} />
						</GroupRow>
					) : servers.data.servers.length === 0 ? (
						<GroupRow
							icon="wrench"
							title={t("assistantSettings.mcpTitle")}
							meta={t("assistantSettings.mcpEmpty")}
						/>
					) : (
						servers.data.servers.map((s) => (
							<ServerRow
								key={s.name}
								server={s}
								enabled={!current.disabledMcpServers.includes(s.name)}
								disabled={busy}
								researchTools={
									research.data?.settings.mcpTools[s.name]?.length ?? 0
								}
								onToggle={(on) => setServer(s.name, on)}
								onResearchTools={() => setToolsFor(s.name)}
							/>
						))
					)}
				</GroupList>
				<GroupNote>{t("assistantSettings.mcpHint")}</GroupNote>
				{putError("web")}
				{toolsFor && (
					<McpResearchToolsDialog
						server={toolsFor}
						onClose={() => setToolsFor(null)}
					/>
				)}
			</Group>
			<ResearchCard />
			<Group title={t("assistantSettings.computerTitle")}>
				<GroupList>
					<GroupRow
						icon="box"
						title={t("assistantSettings.computerProvider")}
						trailing={
							<Select
								id="computer-provider"
								className="w-44"
								aria-label={t("assistantSettings.computerProvider")}
								value={computer.provider ?? ""}
								disabled={busy}
								options={[
									{ value: "", label: t("assistantSettings.computerDefault") },
									...(providers.data?.providers ?? []).map((p) => ({
										value: p.name,
										label: p.name,
									})),
								]}
								onChange={(v) =>
									// 换 provider 时类型回到默认（各 provider 的类型表不同）
									save("computer", { computer: v ? { provider: v } : {} })
								}
							/>
						}
					/>
					<GroupRow
						icon="cpu"
						title={t("assistantSettings.computerOrbType")}
						trailing={
							<Select
								id="computer-orb-type"
								className="w-44"
								aria-label={t("assistantSettings.computerOrbType")}
								value={computer.orbType ?? ""}
								disabled={busy}
								options={[
									{ value: "", label: t("assistantSettings.computerDefault") },
									...(provider?.orbTypes ?? []).map((ot) => ({
										value: ot.name,
										label: typeHasDocker(ot)
											? `${ot.name} · ${t("accel.docker")}`
											: ot.name,
									})),
								]}
								onChange={(v) =>
									save("computer", {
										computer: { ...computer, orbType: v || undefined },
									})
								}
							/>
						}
					/>
				</GroupList>
				<GroupNote>{t("assistantSettings.computerHint")}</GroupNote>
				{putError("computer")}
			</Group>
		</>
	);
}
