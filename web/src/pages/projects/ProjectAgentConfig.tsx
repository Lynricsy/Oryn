/**
 * 项目页的「Agent 配置」卡片（M12.2）：浏览、编辑、上传该项目的中央 omp 配置层。
 * 编辑 UI 与设置页的全局 Agent 配置同一套（`FileBrowser` + `projectAgentConfigSource`）；
 * 项目层不做密钥抽取，写入结果只有 `possible_secret` 之类的警告，逐条显示在卡片下方。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Project, Schemas } from "../../api/client";
import { FileBrowser } from "../../components/files/FileBrowser";
import { Callout, Card } from "../../components/ui/data";
import { projectAgentConfigSource } from "../../lib/file-source";

export function ProjectAgentConfigCard({
	project,
}: {
	project: Project;
}): ReactNode {
	const { t } = useTranslation();
	const [result, setResult] = useState<
		Schemas["AgentConfigWriteResult"] | null
	>(null);
	// 来源须稳定：FileBrowser 以 sources 为依赖加载根
	const sources = useMemo(
		() => [projectAgentConfigSource(project.id, setResult)],
		[project.id],
	);
	return (
		<Card title={t("projectsPage.agentConfig.title")} icon="file">
			<p className="mb-3 text-muted text-xs">
				{t("projectsPage.agentConfig.hint")}{" "}
				{t("projectsPage.agentConfig.secretHint")}{" "}
				{t("projectsPage.agentConfig.pushHint")}
			</p>
			<div className="h-[28rem] overflow-hidden rounded-lg border border-border">
				<FileBrowser
					sources={sources}
					storageKey={`project.${project.id}.agent-config`}
				/>
			</div>
			{result && result.warnings.length > 0 && (
				<div className="mt-2 flex flex-col gap-1.5">
					{result.warnings.map((w) => (
						<Callout key={`${w.path}:${w.line ?? 0}:${w.kind}`} tone="warn">
							{`${w.path}${w.line ? `:${w.line}` : ""} — ${w.message}`}
						</Callout>
					))}
				</div>
			)}
		</Card>
	);
}
