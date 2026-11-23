/**
 * orb 环境（计划 I.7）：输入坞托盘里的提示条、setup / resume 日志对话框、信息面板「环境」入口行
 * （行右侧是最近 setup / resume 的结果标签，展开后是快照来源与运行记录）。
 * 提示条的选择与提示词在 `lib/env-banner.ts`。
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Orb } from "../../api/client";
import { qk, useProjects } from "../../api/queries";
import {
	buildEnvPrompt,
	dismissBanner,
	type EnvBanner as EnvBannerData,
	type FailureBanner,
	isFailureBanner,
	loadDismissed,
	type SetupRun,
	selectEnvBanner,
} from "../../lib/env-banner";
import { formatDuration, formatRelative } from "../../lib/format";
import {
	diagnoseSetupRun,
	type SetupDiagnosis,
} from "../../lib/setup-diagnosis";
import {
	createThread,
	fetchSetupRunLog,
	sendGeneratedPrompt,
	wakeThread,
} from "../../state/actions";
import { useThread } from "../../state/stores";
import { showToast } from "../../state/toasts";
import { uiStore } from "../../state/ui";
import { addPendingPrompt } from "../transcript/pending-prompt";
import { CopyButton } from "../ui/CopyButton";
import { Badge, Callout, Stat, StatusDot, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	IconButton,
	Spinner,
} from "../ui/primitives";
import { InfoRow, InfoTag, InfoTags, useExpandedKey } from "./info-parts";
import { useOrbOf } from "./ThreadHeader";

type AgentBanner = Exclude<EnvBannerData, FailureBanner>;
type MissingConfigBanner = Extract<AgentBanner, { kind: "missing_config" }>;

function useProjectOf(threadId: string) {
	const projectId = useThread(threadId, (s) => s.thread?.projectId);
	const orbProjectId = useOrbOf(threadId)?.projectId;
	const projects = useProjects();
	const id = projectId ?? orbProjectId;
	return id ? projects.data?.projects.find((p) => p.id === id) : undefined;
}

/**
 * 失败运行的「原因 + 建议」两句话（`run` 成功或缺省时为 null）；认不出原因时原因就是 orbd 给的原文。
 * `log` 未到达时只按错误串判断，日志到达后再细化。
 */
function useDiagnosisText(
	run: SetupRun | undefined,
	log?: string,
): { cause: string; hint: string } | null {
	const { t } = useTranslation();
	const diag: SetupDiagnosis | null = run ? diagnoseSetupRun(run, log) : null;
	if (!run || !diag) return null;
	const script =
		diag.script === null
			? t("env.diag.projectScript", { phase: t(`env.phase.${run.phase}`) })
			: diag.script
				? t("env.diag.repoScript", { path: diag.script })
				: t("env.diag.thisScript");
	switch (diag.kind) {
		case "timeout":
			return {
				cause: t("env.diag.timeout", { min: diag.limitMin }),
				hint: t("env.diag.timeoutHint"),
			};
		case "cancelled":
			return {
				cause: t("env.diag.cancelled"),
				hint: t("env.diag.cancelledHint"),
			};
		case "notFound":
			return {
				cause: diag.command
					? t("env.diag.notFound", { script, command: diag.command })
					: t("env.diag.notFoundAny", { script }),
				hint: t("env.diag.notFoundHint"),
			};
		case "exit":
			return {
				cause: t("env.diag.exit", { script, code: diag.code }),
				hint: t("env.diag.exitHint"),
			};
		case "unknown":
			return {
				cause: run.error || t("env.noReason"),
				hint: t("env.diag.unknownHint"),
			};
		default:
			return {
				cause: t(`env.diag.${diag.kind}`, { script, pkg: diag.pkg }),
				hint: t(`env.diag.${diag.kind}Hint`),
			};
	}
}

/** 失败运行的日志（与日志对话框共用缓存）；只在需要时取。 */
function useRunLog(orbId: string | undefined, run: SetupRun | undefined) {
	return useQuery({
		queryKey: qk.setupRunLog(orbId ?? "", run?.id ?? ""),
		queryFn: () => fetchSetupRunLog(orbId ?? "", run?.id ?? ""),
		staleTime: Number.POSITIVE_INFINITY,
		enabled: Boolean(orbId && run && !run.ok),
	});
}

/** 「让 agent 修复 / 优化」：取日志尾部、拼提示词、按忙闲以 prompt / follow_up 发到当前线程。 */
function useAskAgent(threadId: string) {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const orbId = useOrbOf(threadId)?.id;
	const project = useProjectOf(threadId);
	return async (banner: Exclude<AgentBanner, MissingConfigBanner>) => {
		let log = "";
		if (orbId) {
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

/**
 * 「新开线程生成」环境配置：在同一项目的新 orb 上建一条可见线程，在后台调查仓库、写出并推送 `.oryn/setup`，
 * 不占用、也不跳离当前线程（当前线程可能正在做别的事，工作区也可能不干净）。
 * 新线程里「还没有环境配置」整类预先关掉：它正在做这件事，不该再提示用户去生成。
 */
function useGenerateInNewThread(threadId: string) {
	const { t } = useTranslation();
	const project = useProjectOf(threadId);
	return async (banner: MissingConfigBanner) => {
		const prompt = buildEnvPrompt(banner, "", project?.baseBranch);
		const thread = await createThread({
			projectId: project?.id,
			prompt,
			executor: { kind: "new_orb" },
			title: t("env.generateTitle"),
		});
		addPendingPrompt(thread.id, prompt);
		dismissBanner(thread.id, banner.kind);
		return thread;
	};
}

const TONE = {
	err: "bg-err/5",
	warn: "bg-warn/8",
} as const;

/** 托盘里的一节：无边框无圆角，带色调底（作为托盘第一节时随托盘圆角）。 */
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
			className={`flex gap-2.5 px-3.5 py-2.5 text-sm first:rounded-t-[calc(var(--r-xl)-1px)] ${TONE[tone]}`}
		>
			<Icon
				name="alert"
				size={16}
				weight="fill"
				className={`mt-0.5 shrink-0 ${tone === "err" ? "text-err" : "text-warn"}`}
			/>
			<div className="min-w-0 flex-1">
				<div className="font-medium">{title}</div>
				{/* 失败时是「原因 + 建议」两句，窄屏各占两行：留 4 行 */}
				{detail && (
					<div className="mt-0.5 line-clamp-4 break-words text-muted text-xs">
						{detail}
					</div>
				)}
				<div className="mt-2 flex flex-wrap items-center gap-1.5">
					{actions}
				</div>
			</div>
			{onDismiss && (
				<IconButton
					icon="close"
					size={14}
					box="sm"
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
	const generate = useGenerateInNewThread(threadId);
	const navigate = useNavigate();
	const [dismissed, setDismissed] = useState(() => loadDismissed(threadId));
	const [pending, setPending] = useState<string | null>(null);
	const [sent, setSent] = useState<string | null>(null);
	const [error, setError] = useState<unknown>(null);
	const banner = selectEnvBanner({ thread, orb, project }, dismissed);
	const failedRun = banner?.kind === "run_failed" ? banner.run : undefined;
	const failedLog = useRunLog(orb?.id, failedRun);
	const diag = useDiagnosisText(failedRun, failedLog.data);
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

	if (isFailureBanner(banner)) {
		// agent 没能启动时同样走 wake：orb 就绪时它直接重新 agent.start（例如 runner 改了服务目录之后）
		return (
			<BannerShell
				tone="err"
				title={t(
					banner.kind === "orb_failed" ? "env.orbFailed" : "env.agentFailed",
				)}
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
			? diag && (
					<>
						<div className="text-muted">{diag.cause}</div>
						<div className="text-faint">{diag.hint}</div>
					</>
				)
			: banner.kind === "slow_setup"
				? t("env.slowHint")
				: t("env.missingHint");
	const wasSent = sent === banner.key;
	const askAgent =
		banner.kind === "missing_config"
			? async () => {
					const created = await generate(banner);
					// 留在当前线程：提示条随即消失，新线程在侧栏里可见，轻提示给出入口
					dismiss();
					showToast({
						text: t("env.generateStarted"),
						action: {
							label: t("env.openThread"),
							run: () => navigate(`/t/${created.id}`),
						},
					});
				}
			: () => ask(banner);
	return (
		<BannerShell
			tone={banner.kind === "run_failed" ? "err" : "warn"}
			title={title}
			detail={detail}
			onDismiss={dismiss}
			actions={
				<>
					{banner.kind !== "missing_config" && (
						<Button size="sm" icon="fileText" onClick={openLog}>
							{t("env.viewLog")}
						</Button>
					)}
					<Button
						size="sm"
						variant="primary"
						icon={wasSent ? "check" : "sparkle"}
						disabled={pending !== null || wasSent}
						onClick={() => void act(banner.key, askAgent)}
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

/** 一次运行的结果：失败 / 跳过 / 成功（返回语气与文案 key）。 */
function runResult(
	run: SetupRun,
): [tone: Tone, key: "env.failed" | "env.skipped" | "env.ok"] {
	if (!run.ok) return ["err", "env.failed"];
	if (run.phase === "setup" && !run.setupRan) return ["neutral", "env.skipped"];
	return ["ok", "env.ok"];
}

/** 一次运行的元信息：耗时、触发方式、结束时间（结果由调用方另行表达，只出现一次）。 */
function RunMeta({ run }: { run: SetupRun }): ReactNode {
	const { t } = useTranslation();
	// 耗时、触发方式、相对时间各带图标，用间距分开，不再用点号拼接
	return (
		<span className="inline-flex min-w-0 items-center gap-3 whitespace-nowrap">
			<Stat icon="timer" label={t("stat.duration")}>
				{formatDuration(run.durationMs)}
			</Stat>
			<span className="inline-flex items-center gap-1">
				<Icon
					name={run.trigger === "manual" ? "user" : "bolt"}
					size={12}
					aria-hidden
					className="shrink-0"
				/>
				{t(`env.trigger.${run.trigger}`)}
			</span>
			<span className="inline-flex items-center gap-1 tabular-nums">
				<Icon name="clock" size={12} aria-hidden className="shrink-0" />
				{formatRelative(run.finishedAt)}
			</span>
		</span>
	);
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
	const [resultTone, resultKey] = runResult(run);
	const ask = useAskAgent(threadId);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const log = useQuery({
		queryKey: qk.setupRunLog(orbId, run.id),
		queryFn: () => fetchSetupRunLog(orbId, run.id),
		staleTime: Number.POSITIVE_INFINITY,
	});
	const diag = useDiagnosisText(run, log.data);
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
			<div className="mb-2 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-faint text-xs">
				<Badge tone={resultTone}>{t(resultKey)}</Badge>
				<RunMeta run={run} />
				<span className="flex-1" />
				{log.data && <CopyButton text={log.data} label={t("common.copy")} />}
			</div>
			{diag && (
				<div className="mb-2">
					<Callout tone="err" title={diag.cause}>
						{diag.hint}
						{/* 已归纳出原因时，orbd 的原文仍留着备查 */}
						{run.error && run.error !== diag.cause && (
							<div className="mt-1 break-words font-mono text-2xs opacity-80">
								{run.error}
							</div>
						)}
					</Callout>
				</div>
			)}
			{log.isPending ? (
				<Spinner />
			) : log.error ? (
				<ErrorText error={log.error} />
			) : log.data ? (
				<pre
					ref={pre}
					data-testid="setup-log"
					className="max-h-[55dvh] overflow-auto rounded-lg border border-border bg-inset p-2.5 font-mono text-2xs leading-snug"
				>
					{log.data}
				</pre>
			) : (
				<p className="text-faint text-sm">{t("env.logEmpty")}</p>
			)}
		</Dialog>
	);
}

/** 「环境」入口行展开后的运行记录：最近一次 setup / resume 与日志入口。 */
function EnvRuns({
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
		<ul className="flex flex-col">
			{present.map((run) => {
				const [tone, key] = runResult(run);
				const label = t(key);
				return (
					<li
						key={run.id}
						className="flex h-8 min-w-0 items-center gap-2 text-xs"
					>
						{/* 状态只由这个点表达一次；悬停给出文字 */}
						<span title={label} className="inline-flex">
							<StatusDot tone={tone} label={label} />
						</span>
						<span className="shrink-0 font-medium text-fg">
							{t(`env.phase.${run.phase}`)}
						</span>
						<span className="flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden whitespace-nowrap text-faint">
							<RunMeta run={run} />
						</span>
						<Button
							size="sm"
							variant="ghost"
							className="-mr-1.5 shrink-0"
							onClick={() =>
								uiStore.getState().open({ kind: "setupLog", orbId, run })
							}
						>
							{t("env.log")}
						</Button>
					</li>
				);
			})}
		</ul>
	);
}

/**
 * 环境的快照来源（M11a）：来自项目快照的 orb 显示「来自项目快照 · 提交 abc1234」（提交取最近一次 setup 结束时的检出）；
 * 本次 setup 起步时已有标记（快照）却仍实际跑了脚本，说明基线有新提交、在快照上增量重跑过。
 */
export function SnapshotOrigin({
	orb,
}: {
	orb: Pick<Orb, "snapshotKind" | "lastSetup">;
}): ReactNode {
	const { t } = useTranslation();
	const run = orb.lastSetup;
	const fromSnapshot = orb.snapshotKind === "project";
	const refreshed = Boolean(run?.prepared && run.setupRan);
	if (!fromSnapshot && !refreshed) return null;
	const commit = run?.baseCommit?.slice(0, 7);
	return (
		<div
			data-testid="env-snapshot"
			className="mb-1 flex flex-col gap-0.5 text-muted text-xs"
		>
			{fromSnapshot && (
				<span className="inline-flex min-w-0 items-center gap-1.5">
					<Icon
						name="snapshot"
						size={12}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<span className="min-w-0 truncate">
						{commit
							? t("accel.fromSnapshotCommit", { commit })
							: t("accel.fromSnapshot")}
					</span>
				</span>
			)}
			{refreshed && (
				<span className="inline-flex min-w-0 items-center gap-1.5">
					<Icon
						name="branch"
						size={12}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<span className="min-w-0">{t("accel.refreshed")}</span>
				</span>
			)}
		</div>
	);
}

/** 「环境」入口行的一个结果标签：成功只给灰底 + 绿勾 + 耗时，跳过与失败写出结果；悬停给出触发方式与时间。 */
function RunTag({ run }: { run: SetupRun }): ReactNode {
	const { t } = useTranslation();
	const [tone, key] = runResult(run);
	const phase = t(`env.phase.${run.phase}`);
	const title = [
		`${phase} · ${t(key)}`,
		formatDuration(run.durationMs),
		t(`env.trigger.${run.trigger}`),
		formatRelative(run.finishedAt),
	].join(" · ");
	if (tone === "ok")
		return (
			<InfoTag title={title}>
				<Icon
					name="check"
					size={11}
					weight="bold"
					aria-hidden
					className="shrink-0 text-ok"
				/>
				{phase} {formatDuration(run.durationMs)}
			</InfoTag>
		);
	return (
		<InfoTag tone={tone} title={title}>
			<span className="min-w-0 truncate">
				{phase} {t(key)}
			</span>
		</InfoTag>
	);
}

/** 信息面板「环境」入口行（runner 不跑 setup / resume，不渲染）。 */
export function EnvRow({ orb }: { orb: Orb }): ReactNode {
	const { t } = useTranslation();
	const { expanded, onToggle } = useExpandedKey("env:expanded");
	const runs = [orb.lastSetup, orb.lastResume];
	const tags = runs
		.filter((r): r is SetupRun => Boolean(r))
		.map((run) => ({ key: run.id, node: <RunTag run={run} /> }));
	return (
		<InfoRow
			icon="wrench"
			title={t("env.title")}
			testId="orb-env"
			expanded={expanded}
			onToggle={onToggle}
			status={<InfoTags tags={tags} />}
		>
			<SnapshotOrigin orb={orb} />
			<EnvRuns orbId={orb.id} runs={runs} />
		</InfoRow>
	);
}
