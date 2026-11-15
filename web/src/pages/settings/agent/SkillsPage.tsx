/**
 * 设置 → Agent → 技能：`skills/<名称>/SKILL.md` 的增删改与启停（`disabledExtensions` 的 `skill:<名称>`）。
 * Core 只加载带 `description` 的技能（`scanSkillsFromDir` 的 `requireDescription`），表单里描述必填。
 */
import { useTranslation } from "react-i18next";
import { AgentScopeNote } from "../../../components/settings/agent-config-ui";
import {
	type FieldSpec,
	MarkdownCollection,
} from "../../../components/settings/MarkdownCollection";
import { skillKey } from "../../../lib/omp-config";

/** 同 Core 托管技能的命名规则（`autolearn/managed-skills.ts`）。 */
const SKILL_NAME = /^[a-z0-9][a-z0-9-]{0,63}$/;

export default function SkillsPage() {
	const { t } = useTranslation();
	const fields: FieldSpec[] = [
		{
			key: "description",
			kind: "textarea",
			label: t("agentPages.fields.description"),
			hint: t("agentPages.skills.descriptionHint"),
			required: true,
		},
	];
	return (
		<>
			<AgentScopeNote />
			<MarkdownCollection
				dir="skills"
				layout="dir-skill"
				keyOf={skillKey}
				fields={fields}
				title={t("agentPages.skills.title")}
				icon="sparkle"
				hint={t("agentPages.skills.hint")}
				emptyText={t("agentPages.skills.empty")}
				namePattern={SKILL_NAME}
				nameHint={t("agentPages.skills.nameHint")}
				requireDescription
			/>
		</>
	);
}
