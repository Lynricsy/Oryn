/**
 * orb 环境（计划 I.7）：输入区上方的提示条、setup / resume 日志对话框、信息面板「环境」小节的运行摘要。
 * 提示条的选择与提示词在 `lib/env-banner.ts`。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { qk, useProjects } from "../../api/queries";
import {
	buildEnvPrompt,
	dismissBanner,
	type EnvBanner as EnvBannerData,
	loadDismissed,
	type SetupRun,
	selectEnvBanner,
} from "../../lib/env-banner";
import { formatDuration, formatRelative } from "../../lib/format";
import {
	fetchSetupRunLog,
	sendGeneratedPrompt,
	wakeThread,
} from "../../state/actions";
import { useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { CopyButton } from "../ui/CopyButton";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	IconButton,
	Spinner,
} from "../ui/primitives";
import { useOrbOf } from "./ThreadHeader";

type AgentBanner = Exclude<EnvBannerData, { kind: "orb_failed" }>;

function useProjectOf(threadId: string) {
	const projectId = useThread(threadId, (s) => s.thread?.projectId);
	const orbProjectId = useOrbOf(threadId)?.projectId;
	const projects = useProjects();
	const id = projectId ?? orbProjectId;
	return id ? projects.data?.projects.find((p) => p.id === id) : undefined;
}

/** 「让 agent 修复 / 优化 / 生成」：取日志尾部、拼提示词、按忙闲以 prompt / follow_up 发送。 */
function useAskAgent(threadId: string) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const orbId = useOrbOf(threadId)?.id;
	const project = useProjectOf(threadId);
	return async (banner: AgentBanner) => {
		let log = "";
		if (banner.kind !== "missing_config" && orbId) {
			const runId = banner.run.id;
			try {
				log = await qc.fetchQuery({
					queryKey: qk.setupRunLog(orbId, runId),
					queryFn: () => fetchSetupRunLog(orbId, runId),
					staleTime: Number.POSITIVE_INFINITY,
				});
			} catch (e) {
				log = `（获取日志失败：${describeError(e, t)}）`;
			}
		}
		await sendGeneratedPrompt(
			threadId,
			buildEnvPrompt(banner, log, project?.baseBranch),
		);
	};
}

const TONE = {
	err: "border-err/40 bg-err/5",
	warn: "border-warn/40 bg-warn/5",
} as const;

function BannerShell({
	tone,
	title,
	detail,
	actions,
	onDismiss,
}: {
	tone: keyof typeof TONE;
	title: ReactNode;
	detail?: ReactNode;
	actions: ReactNode;
	onDismiss?: () => void;
}) {
	const { t } = useTranslation();
	return (
		<div
			role={tone === "err" ? "alert" : "status"}
			data-testid="env-banner"
			className={`mb-2 flex gap-2 rounded-lg border px-3 py-2 text-sm ${TONE[tone]}`}
		>
			<Icon
				name="alert"
				size={15}
				className={`mt-0.5 shrink-0 ${tone === "err" ? "text-err" : "text-warn"}`}
			/>
			<div className="min-w-0 flex-1">
				<div className="font-medium">{title}</div>
				{detail && (
					<div className="mt-0.5 line-clamp-3 break-words text-muted text-xs">
						{detail}
					</div>
				)}
				<div className="mt-2 flex flex-wrap gap-1.5">{actions}</div>
			</div>
			{onDismiss && (
				<IconButton
					icon="close"
					size={14}
					className="-mt-1 -mr-1.5"
					label={t("common.dismiss")}
					onClick={onDismiss}
				/>
			)}
		</div>
	);
}

/** 线程页输入区上方的环境提示条；按线程挂载（父组件以 threadId 作 key）。 */
export function EnvBanner({ threadId }: { threadId: string }): ReactNode {
	const { t } = useTranslation();
	const thread = useThread(threadId, (s) => s.thread);
	const orb = useOrbOf(threadId);
	const project = useProjectOf(threadId);
	const ask = useAskAgent(threadId);
	const [dismissed, setDismissed] = useState(() => loadDismissed(threadId));
	const [pending, setPending] = useState<string | null>(null);
	const [sent, setSent] = useState<string | null>(null);
	const [error, setError] = useState<unknown>(null);
	const banner = selectEnvBanner({ thread, orb, project }, dismissed);
	if (!banner) return null;

	const act = async (key: string, fn: () => Promise<void>) => {
		setPending(key);
		setError(null);
		try {
			await fn();
			setSent(key);
		} catch (e) {
			setError(e);
		} finally {
			setPending(null);
		}
	};
	const dismiss = () => setDismissed(dismissBanner(threadId, banner.key));

	if (banner.kind === "orb_failed") {
		return (
			<BannerShell
				tone="err"
				title={t("env.orbFailed")}
				detail={banner.reason || t("env.noReason")}
				actions={
					<>
						<Button
							size="sm"
							variant="primary"
							disabled={pending !== null}
							onClick={() => void act("wake", () => wakeThread(threadId))}
						>
							{pending === "wake" ? <Spinner size={12} /> : null}
							{t("env.retry")}
						</Button>
						<ErrorText error={error} />
					</>
				}
			/>
		);
	}

	const run = banner.run;
	const openLog = () =>
		orb && uiStore.getState().open({ kind: "setupLog", orbId: orb.id, run });
	const askLabel =
		banner.kind === "run_failed"
			? t("env.askFix")
			: banner.kind === "slow_setup"
				? t("env.askOptimize")
				: t("env.askGenerate");
	const title =
		banner.kind === "run_failed"
			? t(run.phase === "resume" ? "env.resumeFailed" : "env.setupFailed")
			: banner.kind === "slow_setup"
				? t("env.slow", {
						min: Math.max(1, Math.round(run.durationMs / 60_000)),
					})
				: t("env.missing");
	const detail =
		banner.kind === "run_failed"
			? run.error || t("env.noReason")
			: banner.kind === "slow_setup"
				? t("env.slowHint")
				: t("env.missingHint");
	const wasSent = sent === banner.key;
	return (
		<BannerShell
			tone={banner.kind === "run_failed" ? "err" : "warn"}
			title={title}
			detail={detail}
			onDismiss={dismiss}
			actions={
				<>
					{banner.kind !== "missing_config" && (
						<Button size="sm" icon="file" onClick={openLog}>
							{t("env.viewLog")}
						</Button>
					)}
					<Button
						size="sm"
						variant="primary"
						icon={wasSent ? "check" : "sparkle"}
						disabled={pending !== null || wasSent}
						onClick={() => void act(banner.key, () => ask(banner))}
					>
						{pending === banner.key ? <Spinner size={12} /> : null}
						{wasSent ? t("env.sent") : askLabel}
					</Button>
					<ErrorText error={error} />
				</>
			}
		/>
	);
}

/** 一次运行的摘要：阶段 · 结果 · 耗时 · 触发方式 · 时间。 */
function runSummary(run: SetupRun, t: TFunction): string {
	return [
		t(`env.phase.${run.phase}`),
		!run.ok
			? t("env.failed")
			: run.phase === "setup" && !run.setupRan
				? t("env.skipped")
				: t("env.ok"),
		formatDuration(run.durationMs),
		t(`env.trigger.${run.trigger}`),
		formatRelative(run.finishedAt),
	]
		.filter(Boolean)
		.join(" · ");
}

export function SetupLogDialog({
	threadId,
	orbId,
	run,
	onClose,
}: {
	threadId: string;
	orbId: string;
	run: SetupRun;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const ask = useAskAgent(threadId);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const log = useQuery({
		queryKey: qk.setupRunLog(orbId, run.id),
		queryFn: () => fetchSetupRunLog(orbId, run.id),
		staleTime: Number.POSITIVE_INFINITY,
	});
	const pre = useRef<HTMLPreElement>(null);
	// 日志加载后滚到末尾（失败原因通常在最后）
	useEffect(() => {
		if (log.data !== undefined && pre.current)
			pre.current.scrollTop = pre.current.scrollHeight;
	}, [log.data]);
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("env.logTitle", { phase: t(`env.phase.${run.phase}`) })}
			footer={
				<>
					<ErrorText error={error} />
					<Button onClick={onClose}>{t("common.close")}</Button>
					{!run.ok && (
						<Button
							variant="primary"
							icon="sparkle"
							disabled={busy}
							onClick={async () => {
								setBusy(true);
								setError(null);
								try {
									await ask({
										kind: "run_failed",
										key: `failed:${run.id}`,
										run,
									});
									onClose();
								} catch (e) {
									setError(e);
								} finally {
									setBusy(false);
								}
							}}
						>
							{t("env.askFix")}
						</Button>
					)}
				</>
			}
		>
			<div className="mb-2 flex items-center gap-2 text-muted text-xs">
				<span
					className={`h-2 w-2 shrink-0 rounded-full ${run.ok ? "bg-ok" : "bg-err"}`}
				/>
				<span className="min-w-0 flex-1">{runSummary(run, t)}</span>
				{log.data && <CopyButton text={log.data} label={t("common.copy")} />}
			</div>
			{run.error && (
				<p className="mb-2 break-words text-err text-sm">{run.error}</p>
			)}
			{log.isPending ? (
				<Spinner />
			) : log.error ? (
				<ErrorText error={log.error} />
			) : log.data ? (
				<pre
					ref={pre}
					data-testid="setup-log"
					className="max-h-[55dvh] overflow-auto rounded-md border border-border bg-inset p-2 font-mono text-2xs leading-snug"
				>
					{log.data}
				</pre>
			) : (
				<p className="text-faint text-sm">{t("env.logEmpty")}</p>
			)}
		</Dialog>
	);
}

/** 信息面板 orb 区的「环境」小节内容：最近一次 setup / resume 与日志入口。 */
export function EnvRuns({
	orbId,
	runs,
}: {
	orbId: string;
	runs: (SetupRun | undefined)[];
}): ReactNode {
	const { t } = useTranslation();
	const present = runs.filter((r): r is SetupRun => Boolean(r));
	if (present.length === 0)
		return <p className="text-faint text-xs">{t("env.none")}</p>;
	return (
		<ul className="space-y-1">
			{present.map((run) => (
				<li key={run.id} className="flex items-center gap-2 text-xs">
					<span
						className={`h-1.5 w-1.5 shrink-0 rounded-full ${run.ok ? "bg-ok" : "bg-err"}`}
					/>
					<span className="min-w-0 flex-1 truncate text-muted">
						{runSummary(run, t)}
					</span>
					<button
						type="button"
						className="shrink-0 text-accent hover:underline"
						onClick={() =>
							uiStore.getState().open({ kind: "setupLog", orbId, run })
						}
					>
						{t("env.log")}
					</button>
				</li>
			))}
		</ul>
	);
}
