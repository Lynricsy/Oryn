/**
 * 设置 → Agent 各子页顶部的生效状态（R29、R30）：保存后提示已下发；有会话要重启才能生效时列出会话与原因，
 * 给「重启这些会话」；回合中的会话说明空闲时自动重新加载。什么都没有时不渲染。
 */
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type ApplyStatusThread,
	restartSessions,
	useApplyStatus,
	useLastPush,
} from "../../api/agent-config-queries";
import { Badge, Callout } from "../ui/data";
import { Button, ErrorText, Spinner } from "../ui/primitives";

/** 「已下发」的显示时长：R29 承诺 10 秒内写到所有 orb。 */
const PUSHED_VISIBLE_MS = 10_000;

/** `at` 之后 `ms` 毫秒内为 true，到点自动转 false。 */
function useRecent(at: number | undefined, ms: number): boolean {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (at === undefined) return;
		const left = at + ms - Date.now();
		if (left <= 0) return;
		const id = window.setTimeout(() => setNow(Date.now()), left);
		return () => window.clearTimeout(id);
	}, [at, ms]);
	return at !== undefined && now - at < ms;
}

export function ApplyStatusBanner() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const { data } = useApplyStatus();
	const pushed = useRecent(useLastPush(), PUSHED_VISIBLE_MS);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);

	const threads = data?.threads ?? [];
	const pendingReloads = data?.pendingReloads ?? 0;
	if (!pushed && threads.length === 0 && pendingReloads === 0) return null;
	const waiting = threads.some((th) => !th.scheduled);

	const restart = async () => {
		setBusy(true);
		setError(null);
		try {
			await restartSessions(qc);
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	return (
		<div className="flex flex-col gap-1.5">
			{threads.length > 0 && (
				<Callout
					tone="warn"
					icon="restart"
					role="status"
					title={t("agentPages.apply.restartTitle", { count: threads.length })}
					actions={
						waiting && (
							<Button
								variant="primary"
								size="sm"
								disabled={busy}
								onClick={() => void restart()}
							>
								{busy && <Spinner size={12} />}
								{t("agentPages.apply.restart")}
							</Button>
						)
					}
				>
					<p>
						{waiting
							? t("agentPages.apply.restartHint")
							: t("agentPages.apply.scheduledHint")}
					</p>
					<details className="mt-1">
						<summary className="cursor-pointer select-none">
							{t("agentPages.apply.details")}
						</summary>
						<ul className="mt-1 flex flex-col gap-1">
							{threads.map((th) => (
								<ThreadRow key={th.threadId} thread={th} />
							))}
						</ul>
					</details>
					{error !== null && (
						<div className="mt-1">
							<ErrorText error={error} />
						</div>
					)}
				</Callout>
			)}
			{pendingReloads > 0 && (
				<Callout tone="neutral" role="status">
					{t("agentPages.apply.pendingReloads", { count: pendingReloads })}
				</Callout>
			)}
			{pushed && (
				<Callout tone="ok" role="status">
					{t("agentPages.apply.pushed")}
				</Callout>
			)}
		</div>
	);
}

function ThreadRow({ thread }: { thread: ApplyStatusThread }) {
	const { t } = useTranslation();
	return (
		<li className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-fg">
			<span className="min-w-0 truncate font-medium">
				{thread.title || t("agentPages.apply.untitled")}
			</span>
			{thread.scheduled && <Badge>{t("agentPages.apply.scheduled")}</Badge>}
			<span className="flex min-w-0 flex-wrap gap-x-2 text-muted">
				{thread.reasons.map((r) => (
					<code key={r} className="break-all font-mono">
						{r}
					</code>
				))}
			</span>
		</li>
	);
}
