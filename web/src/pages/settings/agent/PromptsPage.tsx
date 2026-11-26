/**
 * 设置 → 技能与指令 › 提示词与上下文：omp-home 顶层的六个提示词文件，各自可新建、编辑、删除（一组，一行一个文件）。
 * - `AGENTS.md` 是用户级上下文文件，启停键 `context-file:user:AGENTS.md`（Core `capability/context-file.ts`，
 *   `loadContextFiles` 以 `level: "user"` 读 agent 目录下的 AGENTS.md）；
 * - `RULES.md` 合成为名为 `RULES` 的常驻规则（`loadStickyRulesFile`），启停键 `rule:RULES`；
 * - `SYSTEM.md` 整段替换内置系统提示词：行上与编辑器里都有警示，存在时分组顶部另有提示。
 * 作用范围说明与下发状态由页面（`InstructionsPage`）统一给出。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAgentFiles, useOmpConfig } from "../../../api/agent-config-queries";
import {
	AgentErrorText,
	ConfirmDeleteDialog,
	GroupNote,
	TextFileDialog,
} from "../../../components/settings/agent-config-ui";
import { Badge, Callout } from "../../../components/ui/data";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
	Toggle,
} from "../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	Reveal,
} from "../../../components/ui/workbench";
import { formatBytes } from "../../../lib/format";
import {
	contextFileKey,
	ruleKey,
	toggleDisabled,
} from "../../../lib/omp-config";

type PromptFile =
	| "AGENTS.md"
	| "RULES.md"
	| "APPEND_SYSTEM.md"
	| "PERSONALITY.md"
	| "TITLE_SYSTEM.md"
	| "SYSTEM.md";

/** `id` 是文案键（文件名里的 `.` 会被 i18next 当成层级分隔）。 */
const FILES = [
	{ path: "AGENTS.md", id: "agents", key: contextFileKey("user", "AGENTS.md") },
	{ path: "RULES.md", id: "rules", key: ruleKey("RULES") },
	{ path: "APPEND_SYSTEM.md", id: "appendSystem" },
	{ path: "PERSONALITY.md", id: "personality" },
	{ path: "TITLE_SYSTEM.md", id: "titleSystem" },
	{ path: "SYSTEM.md", id: "system" },
] as const satisfies readonly { path: PromptFile; id: string; key?: string }[];

export default function PromptsPage() {
	const { t } = useTranslation();
	const files = useAgentFiles();
	const config = useOmpConfig();
	const [editing, setEditing] = useState<PromptFile | null>(null);
	const [deleting, setDeleting] = useState<PromptFile | null>(null);
	const sizes = new Map((files.data ?? []).map((f) => [f.path, f.size]));
	const systemWarning = (
		<Callout tone="warn" title={t("agentPages.context.systemWarningTitle")}>
			{t("agentPages.context.systemWarning")}
		</Callout>
	);

	return (
		<Group title={t("agentPages.context.title")}>
			{sizes.has("SYSTEM.md") && systemWarning}
			{files.error ? (
				<ErrorText error={files.error} />
			) : files.data === undefined ? (
				<Spinner />
			) : (
				<GroupList>
					{FILES.map((f) => {
						const { path, id } = f;
						const key = "key" in f ? f.key : undefined;
						const size = sizes.get(path);
						const exists = size !== undefined;
						return (
							<GroupRow
								key={path}
								icon="fileText"
								title={
									<>
										<span className="min-w-0 truncate font-mono">{path}</span>
										{path === "SYSTEM.md" && (
											<Badge tone="warn" icon="alert">
												{t("agentPages.context.replacesBuiltin")}
											</Badge>
										)}
									</>
								}
								meta={
									<>
										<span>{t(`agentPages.context.files.${id}`)}</span>
										<span aria-hidden className="text-faint">
											·
										</span>
										<span>
											{exists
												? formatBytes(size)
												: t("agentPages.context.missing")}
										</span>
									</>
								}
								trailing={
									<>
										{exists ? (
											<Reveal>
												<IconButton
													icon="edit"
													box="sm"
													size={14}
													label={t("agentPages.edit", { name: path })}
													onClick={() => setEditing(path)}
												/>
												<IconButton
													icon="trash"
													box="sm"
													size={14}
													label={t("agentPages.delete", { name: path })}
													onClick={() => setDeleting(path)}
												/>
											</Reveal>
										) : (
											<Button
												size="sm"
												icon="plus"
												aria-label={t("agentPages.createFile", { name: path })}
												onClick={() => setEditing(path)}
											>
												{t("common.create")}
											</Button>
										)}
										{key && (
											<Toggle
												hideLabel
												checked={!config.disabled.has(key)}
												disabled={!exists || config.doc === undefined}
												onChange={(on) =>
													void config.mutate((doc) =>
														toggleDisabled(doc, key, on),
													)
												}
												label={t("agentPages.enable", { name: path })}
											/>
										)}
									</>
								}
							/>
						);
					})}
				</GroupList>
			)}
			<GroupNote>{t("agentPages.context.hint")}</GroupNote>
			<AgentErrorText error={config.mutateError} />
			{editing && (
				<TextFileDialog
					path={editing}
					title={editing}
					before={editing === "SYSTEM.md" ? systemWarning : undefined}
					onClose={() => setEditing(null)}
				/>
			)}
			{deleting && (
				<ConfirmDeleteDialog
					name={deleting}
					paths={[deleting]}
					onClose={() => setDeleting(null)}
				/>
			)}
		</Group>
	);
}
