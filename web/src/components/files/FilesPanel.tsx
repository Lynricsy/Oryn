/**
 * 线程右侧面板的 Files 标签：根切换器 `Workspace | Home | Agent 配置`。
 * orb 已删除（`orbId` 为空）时只剩 Agent 配置并提示；Agent 配置保存成功后提示「对新 orb 与下次唤醒生效」。
 */
import { type ReactNode, useMemo, useReducer, useState } from "react";
import { useTranslation } from "react-i18next";
import { agentConfigSource, orbFileSource } from "../../lib/file-source";
import { Icon } from "../ui/icons";
import { Button } from "../ui/primitives";
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
				<p className="flex shrink-0 items-center gap-1.5 bg-raised px-3 py-1.5 text-muted text-xs">
					<Icon name="info" size={13} className="shrink-0" />
					{t("files.orbDeleted")}
				</p>
			)}
			{agentSaved && (
				<div className="flex shrink-0 items-center gap-2 bg-ok/10 py-1 pr-1 pl-3 text-ok-ink text-xs">
					<Icon name="checkCircle" size={13} className="shrink-0 text-ok" />
					<span className="min-w-0 flex-1">{t("files.agentConfigSaved")}</span>
					<Button
						size="sm"
						variant="ghost"
						onClick={() => setAgentSaved(false)}
					>
						{t("common.dismiss")}
					</Button>
				</div>
			)}
			<div className="min-h-0 flex-1">
				<FileBrowser
					// 换线程即重建：根、已选文件与目录树都属于该线程（草稿另存，不受影响）
					key={threadId}
					sources={sources}
					storageKey={`thread.${threadId}`}
					refreshKey={refreshKey}
				/>
			</div>
		</div>
	);
}
