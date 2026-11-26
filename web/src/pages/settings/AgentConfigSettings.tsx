/**
 * 设置 → 配置文件（default export，由设置页接入）：与线程 Files 面板同一个文件浏览器 + CodeMirror 编辑器，不做表单化；
 * 保存后显示提取出的密钥与告警。页顶与其它 Agent 子页一样给出作用范围说明与下发状态。
 * 文件浏览器的高度：手机上随视口（70dvh，至少 20rem），宽屏固定 32rem。
 */
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { noteAgentConfigWrite } from "../../api/agent-config-queries";
import type { Schemas } from "../../api/client";
import { identityKeys } from "../../api/identity-queries";
import { invalidateModelCatalog } from "../../api/model-catalog-queries";
import { FileBrowser } from "../../components/files/FileBrowser";
import {
	AgentPageHead,
	GroupNote,
} from "../../components/settings/agent-config-ui";
import { Callout } from "../../components/ui/data";
import { Group } from "../../components/ui/workbench";
import { agentConfigSource } from "../../lib/file-source";

/** omp 主配置文件：其中的 `cycleOrder` / `modelTags` 决定角色头像列表。 */
const ROLES_CONFIG = "config.yml";
/** 中央模型目录按这两个文件渲染：保存或删除后让目录重新读取（服务端按新配置重跑）。 */
const CATALOG_CONFIGS = new Set([ROLES_CONFIG, "models.yml"]);

export default function AgentConfigSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [result, setResult] = useState<
		Schemas["AgentConfigWriteResult"] | null
	>(null);
	// 来源须稳定：FileBrowser 以 sources 为依赖加载根。config.yml 决定 role 列表（身份与头像 → 角色头像），
	// 保存或删除它之后让那份列表重新取；models.yml / config.yml 变了让模型目录重新取；任何改动都会下发，稍后重取生效状态
	const sources = useMemo(() => {
		const changed = (path: string) => {
			if (path === ROLES_CONFIG)
				void qc.invalidateQueries({ queryKey: identityKeys.roles });
			if (CATALOG_CONFIGS.has(path)) void invalidateModelCatalog(qc);
			noteAgentConfigWrite(qc);
		};
		const base = agentConfigSource((r) => {
			setResult(r);
			changed(r.file.path);
		});
		return [
			{
				...base,
				async remove(path: string) {
					await base.remove(path);
					changed(path);
				},
			},
		];
	}, [qc]);
	return (
		<>
			<AgentPageHead>{t("agentConfig.scopeConfigYml")}</AgentPageHead>
			<Group title={t("agentConfig.title")}>
				<div className="glass h-[70dvh] min-h-80 overflow-hidden rounded-[22px] sm:h-[32rem]">
					<FileBrowser sources={sources} storageKey="settings.agent-config" />
				</div>
				{result && (
					<div className="flex flex-col gap-1.5">
						{result.extractedSecrets.length > 0 && (
							<Callout tone="ok">
								{t("agentConfig.extracted", {
									names: result.extractedSecrets.join(", "),
								})}
							</Callout>
						)}
						{result.warnings.map((w) => (
							<Callout key={`${w.path}:${w.line ?? 0}:${w.kind}`} tone="warn">
								{`${w.path}${w.line ? `:${w.line}` : ""} — ${w.message}`}
							</Callout>
						))}
					</div>
				)}
				<GroupNote>
					{t("agentConfig.hint")} {t("agentConfig.pushHint")}
				</GroupNote>
			</Group>
		</>
	);
}
