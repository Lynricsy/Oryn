/**
 * 设置 → Agent → 提示词与上下文：omp-home 顶层的六个提示词文件，各自可新建、编辑、删除。
 * - `AGENTS.md` 是用户级上下文文件，启停键 `context-file:user:AGENTS.md`（Core `capability/context-file.ts`，
 *   `loadContextFiles` 以 `level: "user"` 读 agent 目录下的 AGENTS.md）；
 * - `RULES.md` 合成为名为 `RULES` 的常驻规则（`loadStickyRulesFile`），启停键 `rule:RULES`；
 * - `SYSTEM.md` 整段替换内置系统提示词：行上与编辑器里都有警示，存在时卡片顶部另有提示。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAgentFiles, useOmpConfig } from "../../../api/agent-config-queries";
import {
	AgentErrorText,
	AgentScopeNote,
	ConfirmDeleteDialog,
	TextFileDialog,
} from "../../../components/settings/agent-config-ui";
import { Badge, Callout, Card, ListRow } from "../../../components/ui/data";
import { Icon } from "../../../components/ui/icons";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
	Toggle,
} from "../../../components/ui/primitives";
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

	let body: ReactNode;
	if (files.error) body = <ErrorText error={files.error} />;
	else if (files.data === undefined)
		body = (
			<div className="flex justify-center py-6">
				<Spinner />
			</div>
		);
	else
		body = (
			<div className="-mx-2 flex flex-col">
				{FILES.map((f) => {
					const { path, id } = f;
					const key = "key" in f ? f.key : undefined;
					const size = sizes.get(path);
					const exists = size !== undefined;
					return (
						<ListRow
							key={path}
							leading={
								<Icon name="fileText" size={16} className="text-faint" />
							}
							title={
								<>
									<span className="min-w-0 truncate font-medium font-mono">
										{path}
									</span>
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
									<span aria-hidden>·</span>
									<span>
										{exists
											? formatBytes(size)
											: t("agentPages.context.missing")}
									</span>
								</>
							}
							trailing={
								<>
									{key && (
										<Toggle
											checked={!config.disabled.has(key)}
											disabled={!exists || config.doc === undefined}
											onChange={(on) =>
												void config.mutate((doc) =>
													toggleDisabled(doc, key, on),
												)
											}
											label={
												<span className="sr-only">
													{t("agentPages.enable", { name: path })}
												</span>
											}
										/>
									)}
									{exists ? (
										<>
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
										</>
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
								</>
							}
						/>
					);
				})}
			</div>
		);

	return (
		<>
			<AgentScopeNote />
			<Card title={t("agentPages.context.title")} icon="fileText">
				<p className="mb-2 text-faint text-xs">
					{t("agentPages.context.hint")}
				</p>
				{sizes.has("SYSTEM.md") && <div className="mb-2">{systemWarning}</div>}
				{body}
				<AgentErrorText error={config.mutateError} />
			</Card>
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
		</>
	);
}
