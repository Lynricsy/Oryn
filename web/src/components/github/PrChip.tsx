/**
 * 线程页头的 PR 徽标（M14）：线程关联的 GitHub PR——图标（已合并用 merge，其余 pullRequest）、`#编号`（页头够宽时）、
 * CI 汇总的状态点。关闭与草稿用弱化前景色；已合并保持默认前景色（不用强调色）。点开是菜单：在 GitHub 打开、
 * 暂停 / 恢复事件回流、取消关联。线程随 `index.patch` 更新，操作后不需要手动刷新。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import {
	setThreadPullListening,
	unlinkThreadPull,
} from "../../api/github-queries";
import { StatusDot } from "../ui/data";
import { Icon } from "../ui/icons";
import { Menu } from "../ui/primitives";

type ThreadPull = Schemas["ThreadPull"];

/** 与 `ScheduleChip` 的胶囊同一串类名。 */
const CHIP =
	"inline-flex h-[26px] shrink-0 items-center gap-1.5 rounded-full border px-2 font-medium text-xs transition-colors duration-150";

const CHECK_TONE = { success: "ok", failure: "err", pending: "warn" } as const;

/** PR 的状态文案（草稿单独一档）。 */
export function prStateText(pull: ThreadPull, t: TFunction): string {
	if (pull.state === "open" && pull.draft) return t("thread.pr.state.draft");
	return t(`thread.pr.state.${pull.state}`);
}

/** 状态与 CI 的一行说明：「打开 · CI 失败」；没有 CI 汇总时只有状态。 */
export function prStatusText(pull: ThreadPull, t: TFunction): string {
	const state = prStateText(pull, t);
	return pull.checks
		? `${state} · ${t(`thread.pr.checks.${pull.checks}`)}`
		: state;
}

export function prIcon(pull: ThreadPull): "merge" | "pullRequest" {
	return pull.state === "merged" ? "merge" : "pullRequest";
}

export function openPull(pull: ThreadPull): void {
	window.open(pull.url, "_blank", "noopener");
}

export function PrChip({
	threadId,
	pull,
}: {
	threadId: string;
	pull: ThreadPull;
}): ReactNode {
	const { t } = useTranslation();
	const [error, setError] = useState<string | null>(null);
	const faint = pull.state === "closed" || pull.draft;
	const label = t("thread.pr.label", {
		number: pull.number,
		status: prStatusText(pull, t),
	});
	const run = (p: Promise<unknown>) => {
		setError(null);
		p.catch((e: unknown) =>
			setError(e instanceof Error ? e.message : String(e)),
		);
	};
	return (
		<>
			<Menu
				className="shrink-0"
				align="left"
				items={[
					{
						key: "open",
						icon: "external",
						label: t("thread.pr.open"),
						onSelect: () => openPull(pull),
					},
					{
						key: "listen",
						icon: pull.listening ? "pause" : "play",
						label: pull.listening
							? t("thread.pr.pause")
							: t("thread.pr.resume"),
						onSelect: () =>
							run(
								setThreadPullListening(threadId, pull.number, !pull.listening),
							),
					},
					{
						key: "unlink",
						icon: "close",
						label: t("thread.pr.unlink"),
						onSelect: () => run(unlinkThreadPull(threadId, pull.number)),
					},
				]}
				trigger={(p) => (
					<button
						type="button"
						{...p}
						data-testid="pr-chip"
						data-state={pull.state}
						aria-label={label}
						title={label}
						className={`${CHIP} border-border bg-panel/60 hover:bg-hover ${faint ? "text-faint" : "text-fg"}`}
					>
						<Icon name={prIcon(pull)} size={12} aria-hidden />
						<span aria-hidden className="hidden tabular-nums @md/header:inline">
							{`#${pull.number}`}
						</span>
						{pull.checks && (
							<span data-checks={pull.checks} className="inline-flex">
								<StatusDot tone={CHECK_TONE[pull.checks]} />
							</span>
						)}
					</button>
				)}
			/>
			{error && (
				<span className="max-w-40 truncate text-err text-xs" title={error}>
					{error}
				</span>
			)}
		</>
	);
}
