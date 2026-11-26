/**
 * 设置页「agent 配置」（default export，由设置页接入）：与线程 Files 面板同一个文件浏览器 + CodeMirror 编辑器，不做表单化；
 * 保存后显示提取出的密钥与告警。
 */
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { noteAgentConfigWrite } from "../../api/agent-config-queries";
import type { Schemas } from "../../api/client";
import { identityKeys } from "../../api/identity-queries";
import { invalidateModelCatalog } from "../../api/model-catalog-queries";
import { FileBrowser } from "../../components/files/FileBrowser";
import { ApplyStatusBanner } from "../../components/settings/ApplyStatusBanner";
import { Callout, Card } from "../../components/ui/data";
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
			<ApplyStatusBanner />
			<Card title={t("agentConfig.title")} icon="file">
				<p className="mb-3 text-muted text-xs">
					{t("agentConfig.hint")} {t("agentConfig.pushHint")}
				</p>
				<div className="h-[32rem] overflow-hidden rounded-lg border border-border">
					<FileBrowser sources={sources} storageKey="settings.agent-config" />
				</div>
				{result && (
					<div className="mt-2 flex flex-col gap-1.5">
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
			</Card>
		</>
	);
}
