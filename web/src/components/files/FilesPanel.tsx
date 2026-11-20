/**
 * 线程右侧面板的 Files 标签：根切换器 `Workspace | Home | 项目 Agent 配置 | Agent 配置`（线程属于项目时才有项目配置）。
 * orb 已删除（`orbId` 为空）时只剩两份 Agent 配置并提示；任一份 Agent 配置保存成功后提示「对新 orb 与下次唤醒生效」。
 * orb 里的文件可「发布为公开链接」（复制成附件后发布，见 `usePublishOrbFile`）。
 * orb 已停机（`stopped`）时改用只读的停机预览（不唤醒 orb），顶部提示并给出「唤醒以编辑」；
 * orb 离开停机状态后自动换回在线来源。停机预览不可用（provider 不支持 / 不在线）时就地提示。
 */
import { type ReactNode, useMemo, useReducer, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../api/client";
import { usePublishOrbFile } from "../../api/share-queries";
import {
	agentConfigSource,
	orbFileSource,
	projectAgentConfigSource,
} from "../../lib/file-source";
import { wakeThread } from "../../state/actions";
import { useThread } from "../../state/stores";
import { uiStore, useUi } from "../../state/ui";
import { Icon } from "../ui/icons";
import { Button, ErrorText, Spinner } from "../ui/primitives";
import { FileBrowser, type FilePublisher } from "./FileBrowser";
import { useWorkspaceActivity } from "./hooks";

/** 「唤醒以编辑」：唤醒线程的 orb（与桌面面板、线程头部同一个 `wake`）。 */
function WakeButton({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	return (
		<>
			<Button
				size="sm"
				variant="ghost"
				icon={busy ? undefined : "play"}
				disabled={busy}
				onClick={async () => {
					setBusy(true);
					setError(null);
					try {
						await wakeThread(threadId);
					} catch (e) {
						setError(e);
					} finally {
						setBusy(false);
					}
				}}
			>
				{busy && <Spinner size={12} />}
				{t("files.offline.wake")}
			</Button>
			<ErrorText error={error} />
		</>
	);
}

export default function FilesPanel({
	threadId,
	orbId,
	projectId,
}: {
	threadId: string;
	orbId?: string;
	/** 线程所属项目：有则多一个项目级 Agent 配置的根（M12.2）。 */
	projectId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const [agentSaved, setAgentSaved] = useState(false);
	const orbState = useThread(threadId, (s) => s.orb?.state);
	const offline = orbId !== undefined && orbState === "stopped";
	const sources = useMemo(() => {
		const saved = () => setAgentSaved(true);
		return [
			...(orbId ? [orbFileSource(orbId, threadId, { offline })] : []),
			...(projectId ? [projectAgentConfigSource(projectId, saved)] : []),
			agentConfigSource(saved),
		];
	}, [orbId, threadId, projectId, offline]);
	const [refreshKey, bump] = useReducer((n: number) => n + 1, 0);
	const publishFile = usePublishOrbFile(orbId ?? "");
	// 发布要从 orb 里复制文件，会唤醒 orb：停机预览时不提供
	const publisher: FilePublisher | undefined =
		orbId && !offline
			? {
					sourceId: `orb:${orbId}`,
					run: async (path) =>
						(await publishFile.mutateAsync({ path, threadId })).url,
				}
			: undefined;
	const renderError = (error: unknown, retry: () => void): ReactNode => {
		if (!(error instanceof ApiError)) return null;
		if (error.code === "offline_unsupported")
			return (
				<div className="flex flex-col items-start gap-2 p-3 text-sm">
					<p className="flex items-center gap-1.5 text-muted">
						<Icon name="info" size={13} className="shrink-0" />
						{t("files.offline.unsupported")}
					</p>
					<WakeButton threadId={threadId} />
				</div>
			);
		if (error.code === "provider_unavailable")
			return (
				<div className="flex flex-col items-start gap-2 p-3 text-sm">
					<p className="flex items-center gap-1.5 text-warn">
						<Icon name="alert" size={13} className="shrink-0" />
						{t("files.offline.providerUnavailable")}
					</p>
					<Button size="sm" onClick={retry}>
						{t("files.retry")}
					</Button>
				</div>
			);
		return null;
	};
	useWorkspaceActivity(orbId ? threadId : undefined, bump);
	// 从转录 / 改动列表等处请求打开的文件（`uiStore.openFile`）
	const openPath = useUi((s) => s.fileSel[threadId]);
	return (
		<div className="flex h-full min-h-0 flex-col">
			{!orbId && (
				<p className="flex shrink-0 items-center gap-1.5 bg-raised px-3 py-1.5 text-muted text-xs">
					<Icon name="info" size={13} className="shrink-0" />
					{t("files.orbDeleted")}
				</p>
			)}
			{offline && (
				<div className="flex shrink-0 items-center gap-2 bg-raised py-1 pr-1 pl-3 text-muted text-xs">
					<Icon name="info" size={13} className="shrink-0" />
					<span className="min-w-0 flex-1">{t("files.offline.banner")}</span>
					<WakeButton threadId={threadId} />
				</div>
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
					publisher={publisher}
					renderError={renderError}
					openPath={openPath}
					onOpened={() => uiStore.getState().clearFileSel(threadId)}
				/>
			</div>
		</div>
	);
}
