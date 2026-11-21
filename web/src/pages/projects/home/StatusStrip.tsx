/**
 * 项目主页的状态条（M24）：等你回复 / 工作中的线程数、打开的 PR（带失败的 CI 数）、最近一次 setup 失败、近 7 天成本与回合。
 * 统计范围是索引里本项目、非协调、未归档的线程；数值为 0 的胶囊不显示，全部为 0 时整条不渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Project, Thread } from "../../../api/client";
import { useOrbs } from "../../../api/queries";
import { useUsageStats } from "../../../api/stats-queries";
import { CHIP } from "../../../components/github/PrChip";
import {
	DEFAULT_PARAMS,
	statsSearch,
	statsWindow,
} from "../../../components/stats/statsParams";
import { Icon } from "../../../components/ui/icons";
import { ThreadStateIcon } from "../../../components/ui/primitives";
import { formatCost } from "../../../lib/format";
import { isCoordinatorThread } from "../../../lib/thread-kind";
import { useIndex } from "../../../state/stores";
import { latestSetupOrb } from "../project-orbs";

const CHIP_CLASS = `${CHIP} border-border bg-panel/60 text-fg hover:bg-hover`;

/** 打开的 PR 数与其中 CI 失败的数（按 `repo#number` 去重）。 */
function openPulls(threads: readonly Thread[]): {
	open: number;
	failing: number;
} {
	const seen = new Map<string, boolean>();
	for (const th of threads)
		for (const p of th.pulls ?? [])
			if (p.state === "open")
				seen.set(`${p.repo}#${p.number}`, p.checks === "failure");
	return {
		open: seen.size,
		failing: [...seen.values()].filter(Boolean).length,
	};
}

export function StatusStrip({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const id = project.id;
	const index = useIndex((s) => s.threads);
	const orbs = useOrbs().data?.orbs ?? [];
	const w = statsWindow({ range: "7d" }, Date.now());
	const usage = useUsageStats({
		from: w.from,
		to: w.to,
		tz: w.tz,
		groupBy: "total",
		project: id,
	});

	const live = Object.values(index).filter(
		(th) => th.projectId === id && !isCoordinatorThread(th) && !th.archivedAt,
	);
	const awaiting = live.filter((th) => th.state === "awaiting_input").length;
	const working = live.filter(
		(th) => th.state === "working" || th.state === "provisioning",
	).length;
	const prs = openPulls(live);
	const setupOrb = latestSetupOrb(
		orbs.filter((o) => o.projectId === id && !o.deletedAt),
	);
	const setupFailed = setupOrb?.lastSetup.ok === false;
	const total = usage.data?.total;
	const showUsage = !!total && (total.costUsd > 0 || total.turns > 0);
	if (!awaiting && !working && !prs.open && !setupFailed && !showUsage)
		return null;

	const active = `/projects/${id}/threads?status=active`;
	return (
		// biome-ignore lint/a11y/useSemanticElements: 一组链接胶囊，不是表单字段集
		<div
			data-testid="project-status"
			role="group"
			aria-label={t("projectHome.status")}
			className="flex flex-wrap items-center gap-2"
		>
			{awaiting > 0 && (
				<Link to={active} data-chip="awaiting" className={CHIP_CLASS}>
					<ThreadStateIcon state="awaiting_input" size={12} />
					{t("projectHome.chip.awaiting", { count: awaiting })}
				</Link>
			)}
			{working > 0 && (
				<Link to={active} data-chip="working" className={CHIP_CLASS}>
					<ThreadStateIcon state="working" size={12} />
					{t("projectHome.chip.working", { count: working })}
				</Link>
			)}
			{prs.open > 0 && (
				<Link to={active} data-chip="prs" className={CHIP_CLASS}>
					<Icon name="pullRequest" size={12} aria-hidden />
					{t("projectHome.chip.prs", { count: prs.open })}
					{prs.failing > 0 && (
						<span className="text-err">
							{" · "}
							{t("projectHome.chip.ciFailing", { count: prs.failing })}
						</span>
					)}
				</Link>
			)}
			{setupFailed && (
				<Link
					to={
						setupOrb.threadIds[0]
							? `/t/${setupOrb.threadIds[0]}`
							: `/projects/${id}/settings/environment`
					}
					data-chip="setup"
					className={CHIP_CLASS}
				>
					<Icon name="xCircle" size={12} aria-hidden className="text-err" />
					<span className="text-err">{t("projectHome.chip.setupFailed")}</span>
				</Link>
			)}
			{showUsage && (
				<Link
					to={`/stats/usage?${statsSearch({ ...DEFAULT_PARAMS, range: "7d", project: id })}`}
					data-chip="usage"
					className={CHIP_CLASS}
				>
					<Icon name="chart" size={12} aria-hidden />
					{t("projectHome.chip.usage", {
						count: total.turns,
						cost: formatCost(total.costUsd),
					})}
				</Link>
			)}
		</div>
	);
}
