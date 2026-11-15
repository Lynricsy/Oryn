/**
 * runner 离线提示（M11b）：线程所在 orb 是 runner 且处于 `stopped`（link 断开超过 30 秒）时，
 * 在输入坞托盘里、输入区之上显示一行。消息照常发：控制面排队，runner 重新连接后按序送达。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../ui/icons";
import { useOrbOf } from "./ThreadHeader";

export function RunnerOfflineNotice({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const orb = useOrbOf(threadId);
	if (orb?.kind !== "runner" || orb.state !== "stopped") return null;
	return (
		<div
			role="status"
			data-testid="runner-offline"
			className="flex items-center gap-2 px-5 py-2.5 text-muted text-xs first:rounded-t-[calc(var(--r-xl)-1px)]"
		>
			<Icon name="plugs" size={14} className="shrink-0 text-warn" />
			<span className="min-w-0">
				{t("runner.offlineNotice", { name: orb.providerName ?? "runner" })}
			</span>
		</div>
	);
}
