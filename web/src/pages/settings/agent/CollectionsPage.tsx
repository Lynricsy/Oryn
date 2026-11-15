/**
 * 设置 → Agent → 命令与规则：`commands/`、`rules/`、`prompts/`、`instructions/` 四类 Markdown 条目。
 * 名称与启停键对照 Core `discovery/builtin.ts`：
 * - 命令 `commands/*.md`，名称为文件名去 `.md`，键 `slash-command:<名称>`；frontmatter `description`、`argument-hint`；
 * - 规则 `rules/*.{md,mdc}`，名称为文件名去扩展名，键 `rule:<名称>`；frontmatter `description`、`globs`、`alwaysApply`；
 * - 提示片段 `prompts/*.md`，名称为文件名去 `.md`，键 `prompt:<名称>`；
 * - instruction `instructions/*.md`，名称去 `.instructions.md` 或 `.md`，键 `instruction:<名称>`；frontmatter `applyTo`。
 */
import { useTranslation } from "react-i18next";
import { AgentScopeNote } from "../../../components/settings/agent-config-ui";
import { MarkdownCollection } from "../../../components/settings/MarkdownCollection";
import {
	instructionKey,
	promptKey,
	ruleKey,
	slashCommandKey,
} from "../../../lib/omp-config";

const RULE_EXTENSIONS = ["md", "mdc"] as const;

/** `loadInstructions`：先去 `.instructions.md`，再去 `.md`。 */
function instructionName(file: string): string {
	return file.replace(/\.instructions\.md$/, "").replace(/\.md$/, "");
}

export default function CollectionsPage() {
	const { t } = useTranslation();
	const description = {
		key: "description",
		kind: "text",
		label: t("agentPages.fields.description"),
	} as const;
	return (
		<>
			<AgentScopeNote />
			<MarkdownCollection
				dir="commands"
				layout="flat-md"
				keyOf={slashCommandKey}
				fields={[
					description,
					{
						key: "argument-hint",
						kind: "text",
						label: t("agentPages.fields.argumentHint"),
						hint: t("agentPages.fields.argumentHintHint"),
					},
				]}
				title={t("agentPages.commands.title")}
				icon="terminal"
				hint={t("agentPages.commands.hint")}
				emptyText={t("agentPages.commands.empty")}
			/>
			<MarkdownCollection
				dir="rules"
				layout="flat-md"
				extensions={RULE_EXTENSIONS}
				keyOf={ruleKey}
				fields={[
					description,
					{
						key: "globs",
						kind: "list",
						label: t("agentPages.fields.globs"),
						hint: t("agentPages.fields.globsHint"),
					},
					{
						key: "alwaysApply",
						kind: "bool",
						label: t("agentPages.fields.alwaysApply"),
						hint: t("agentPages.fields.alwaysApplyHint"),
					},
				]}
				title={t("agentPages.rules.title")}
				icon="list"
				hint={t("agentPages.rules.hint")}
				emptyText={t("agentPages.rules.empty")}
			/>
			<MarkdownCollection
				dir="prompts"
				layout="flat-md"
				keyOf={promptKey}
				fields={[description]}
				title={t("agentPages.prompts.title")}
				icon="message"
				hint={t("agentPages.prompts.hint")}
				emptyText={t("agentPages.prompts.empty")}
			/>
			<MarkdownCollection
				dir="instructions"
				layout="flat-md"
				keyOf={instructionKey}
				nameFromFile={instructionName}
				fields={[
					{
						key: "applyTo",
						kind: "text",
						label: t("agentPages.fields.applyTo"),
						hint: t("agentPages.fields.applyToHint"),
					},
				]}
				title={t("agentPages.instructions.title")}
				icon="fileText"
				hint={t("agentPages.instructions.hint")}
				emptyText={t("agentPages.instructions.empty")}
			/>
		</>
	);
}
