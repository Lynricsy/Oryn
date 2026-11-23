/** 转录末尾的工作状态行：环境准备 / 启动 agent、agent 退出、工作中（含当前活动）。 */
import { type ReactNode, useContext } from "react";
import { useTranslation } from "react-i18next";
import { useThread } from "../../state/stores";
import { useOrbOf } from "../thread/ThreadHeader";
import { Callout } from "../ui/data";
import { OrbBead } from "../ui/OrbBead";
import { Spinner } from "../ui/primitives";
import { AwaitingReplyContext } from "./context";

export function WorkingFooter({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const running = useThread(threadId, (s) => s.running);
	const live = useThread(
		threadId,
		(s) => s.liveMessage && !s.liveMessage.ended,
	);
	const state = useThread(threadId, (s) => s.thread?.state);
	const activity = useThread(threadId, (s) => s.thread?.activity);
	const agentStatus = useThread(threadId, (s) => s.agentStatus);
	const orb = useOrbOf(threadId);
	// 转录已在末尾画了回合头 + 三点占位（回合开始、agent 尚无输出）：不再重复画「工作中」
	const awaiting = useContext(AwaitingReplyContext);
	if (state === "provisioning") {
		return (
			<div className="flex items-center gap-2 py-3 text-muted text-sm">
				{orb ? <OrbBead state={orb.state} size={16} /> : <Spinner />}
				{/* orb 已就绪但线程仍在准备：剩下的是拉起 agent */}
				{orb?.state === "ready"
					? t("thread.startingAgent")
					: t("thread.provisioning", {
							state: orb ? t(`orbState.${orb.state}`) : "…",
						})}
			</div>
		);
	}
	// orb 停机时 agent 也已退出：状态与唤醒入口在信息面板的 orb 卡里，这里不再提示
	if (!running && (orb?.state === "stopped" || orb?.state === "stopping")) {
		return <div className="h-4" />;
	}
	if (agentStatus === "exited") {
		return (
			<div className="py-3">
				<Callout tone="warn">{t("thread.agentExited")}</Callout>
			</div>
		);
	}
	if (running && !live && !awaiting) {
		return (
			<div className="flex min-w-0 items-center gap-2 py-3 text-muted text-sm">
				<Spinner />
				<span className="shrink-0">{t("thread.working")}</span>
				{state === "working" && activity && (
					<span className="min-w-0 truncate text-faint text-xs">
						{activity}
					</span>
				)}
			</div>
		);
	}
	return <div className="h-4" />;
}
