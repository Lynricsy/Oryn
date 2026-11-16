/** 转录末尾的工作状态行：环境准备 / 启动 agent、orb 停机、agent 退出、工作中（含当前活动）。 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { wakeThread } from "../../state/actions";
import { useThread } from "../../state/stores";
import { useOrbOf } from "../thread/ThreadHeader";
import { Callout } from "../ui/data";
import { OrbBead } from "../ui/OrbBead";
import { Button, ErrorText, Spinner } from "../ui/primitives";

/** orb 停机时打开线程页不会自动唤醒（冷启动代价高）：说明现状，给出唤醒入口。 */
function StoppedNotice({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const wake = async () => {
		setPending(true);
		setError(null);
		try {
			await wakeThread(threadId);
		} catch (e) {
			setError(e);
		} finally {
			setPending(false);
		}
	};
	return (
		<div className="py-3">
			<Callout
				tone="neutral"
				icon="info"
				actions={
					<Button size="sm" disabled={pending} onClick={() => void wake()}>
						{pending ? <Spinner size={12} /> : null}
						{t("thread.orbWake")}
					</Button>
				}
			>
				{t("thread.orbStopped")}
				{error !== null && <ErrorText error={error} />}
			</Callout>
		</div>
	);
}

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
	if (!running && (orb?.state === "stopped" || orb?.state === "stopping")) {
		return <StoppedNotice threadId={threadId} />;
	}
	if (agentStatus === "exited") {
		return (
			<div className="py-3">
				<Callout tone="warn">{t("thread.agentExited")}</Callout>
			</div>
		);
	}
	if (running && !live) {
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
