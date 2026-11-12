/**
 * 线程右侧面板的 Files 标签：根切换器 `Workspace | Home | Agent 配置`。
 * orb 已删除（`orbId` 为空）时只剩 Agent 配置并提示；Agent 配置保存成功后提示「对新 orb 与下次唤醒生效」。
 */
import { type ReactNode, useMemo, useReducer, useState } from "react";
import { useTranslation } from "react-i18next";
import { agentConfigSource, orbFileSource } from "../../lib/file-source";
import { FileBrowser } from "./FileBrowser";
import { useWorkspaceActivity } from "./hooks";

export default function FilesPanel({
	threadId,
	orbId,
}: {
	threadId: string;
	orbId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const [agentSaved, setAgentSaved] = useState(false);
	const sources = useMemo(() => {
		const agent = agentConfigSource(() => setAgentSaved(true));
		return orbId ? [orbFileSource(orbId), agent] : [agent];
	}, [orbId]);
	const [refreshKey, bump] = useReducer((n: number) => n + 1, 0);
	useWorkspaceActivity(orbId ? threadId : undefined, bump);
	return (
		<div className="flex h-full min-h-0 flex-col">
			{!orbId && (
				<p className="shrink-0 bg-raised px-3 py-1.5 text-muted text-xs">
					{t("files.orbDeleted")}
				</p>
			)}
			{agentSaved && (
				<div className="flex shrink-0 items-center gap-2 bg-ok/10 px-3 py-1.5 text-ok text-xs">
					<span className="min-w-0 flex-1">{t("files.agentConfigSaved")}</span>
					<button
						type="button"
						className="text-muted hover:text-fg"
						onClick={() => setAgentSaved(false)}
					>
						{t("common.dismiss")}
					</button>
				</div>
			)}
			<div className="min-h-0 flex-1">
				<FileBrowser
					sources={sources}
					storageKey={`thread.${threadId}`}
					refreshKey={refreshKey}
				/>
			</div>
		</div>
	);
}
