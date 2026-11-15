/**
 * 新建线程的执行位置：「Runners」分组（M11b）。每个 runner 一行（在线点、名字、主机名），在线的点开后列出它声明的目录；
 * 目录行右侧是「当前目录 / 新 worktree」切换（只有目录本身是 git 仓库时才有）。选中的目录下方可填子路径，
 * 新 worktree 时再给一个名字（缺省取提示词前几个词的 slug）。选择不关闭弹层：子路径与名字就地填写。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { type Runner, useRunnerOnline, useRunners } from "../../api/queries";
import { Badge, StatusDot } from "../ui/data";
import { Icon } from "../ui/icons";
import { PopoverLabel } from "../ui/PickerPopover";

export type RunnerDir = Schemas["RunnerDir"];
export type RunnerMode = "current" | "worktree";

/** 首页选中的 runner 位置。`sub` 是相对 `dir.path` 的子路径；`name` 为空表示用提示词的 slug。 */
export interface RunnerPick {
	runnerId: string;
	runnerName: string;
	dir: RunnerDir;
	sub: string;
	mode: RunnerMode;
	name: string;
}

/** worktree 名（同 orbd 的校验）：字母或数字开头，其余字母、数字、`.`、`_`、`-`，最长 63。 */
const WORKTREE_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/;

/** 提示词前几个词的 slug（只取 ASCII 字母数字；中文等提示词退到 `task`，重名时 orbd 自己加 `-2`…）。 */
export function worktreeSlug(prompt: string): string {
	const words = prompt
		.normalize("NFKD")
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, " ")
		.trim()
		.split(" ")
		.filter(Boolean)
		.slice(0, 4);
	return words.join("-").slice(0, 40).replace(/-+$/, "") || "task";
}

/** 子路径规范化：去掉首尾斜杠；含 `.` / `..` 段或空段时不合法（返回 null）。 */
export function normalizeSub(sub: string): string | null {
	const s = sub.trim().replace(/^\/+|\/+$/g, "");
	if (!s) return "";
	const parts = s.split("/");
	return parts.some((p) => p === "" || p === "." || p === "..")
		? null
		: parts.join("/");
}

/** 选中位置的问题：子路径不合法、worktree 名不合法；没问题为 null。 */
export function runnerPickError(
	pick: RunnerPick,
): "subpath" | "worktreeName" | null {
	if (pick.mode === "current" && normalizeSub(pick.sub) === null)
		return "subpath";
	if (
		pick.mode === "worktree" &&
		pick.name &&
		!WORKTREE_NAME_RE.test(pick.name)
	)
		return "worktreeName";
	return null;
}

/**
 * 线程的工作目录：当前目录模式 = 声明的目录 + 子路径；新 worktree 模式 = 仓库目录本身（worktree 建在仓库根旁，
 * 子路径不适用）。子路径不合法时退回目录本身（发送前已由 `runnerPickError` 拦下）。
 */
export function runnerPickDir(pick: RunnerPick): string {
	const sub = pick.mode === "current" ? normalizeSub(pick.sub) : "";
	return sub ? `${pick.dir.path.replace(/\/+$/, "")}/${sub}` : pick.dir.path;
}

export function runnerExecutor(
	pick: RunnerPick,
	prompt: string,
): Schemas["ThreadExecutorRunner"] {
	return {
		kind: "runner",
		runnerId: pick.runnerId,
		dir: runnerPickDir(pick),
		...(pick.mode === "worktree"
			? { worktree: { name: pick.name || worktreeSlug(prompt) } }
			: {}),
	};
}

/** 两段切换（目录行右侧）：选中项是纸面底 + 墨色字，未选为弱色字。 */
function ModeToggle({
	mode,
	selected,
	onMode,
}: {
	mode: RunnerMode;
	selected: boolean;
	onMode: (mode: RunnerMode) => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<span className="inline-flex shrink-0 rounded-sm bg-inset p-0.5">
			{(["current", "worktree"] as const).map((m) => {
				const on = selected && mode === m;
				return (
					<button
						key={m}
						type="button"
						aria-pressed={on}
						onClick={() => onMode(m)}
						className={`inline-flex h-6 items-center rounded-xs px-1.5 text-2xs transition-colors duration-150 ${on ? "bg-panel font-medium text-fg shadow-xs" : "text-muted hover:text-fg"}`}
					>
						{t(m === "current" ? "runner.modeCurrent" : "runner.modeWorktree")}
					</button>
				);
			})}
		</span>
	);
}

const FIELD =
	"h-7 min-w-0 flex-1 rounded-sm border border-border bg-panel px-2 font-mono text-fg text-xs placeholder:font-sans placeholder:text-faint focus-visible:border-accent focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-accent/20";

/** 选中目录下方的就地设置：子路径、worktree 名。 */
function DirConfig({
	pick,
	prompt,
	onPick,
}: {
	pick: RunnerPick;
	prompt: string;
	onPick: (pick: RunnerPick) => void;
}): ReactNode {
	const { t } = useTranslation();
	const error = runnerPickError(pick);
	return (
		<div
			data-testid="runner-dir-config"
			className="mx-1 mb-1 flex flex-col gap-1.5 rounded-md bg-inset px-2.5 py-2"
		>
			{pick.mode === "current" ? (
				<label className="flex items-center gap-2 text-muted text-xs">
					<span className="w-16 shrink-0">{t("runner.subpath")}</span>
					<input
						className={FIELD}
						value={pick.sub}
						placeholder={t("runner.subpathPlaceholder")}
						aria-invalid={error === "subpath"}
						onChange={(e) => onPick({ ...pick, sub: e.target.value })}
					/>
				</label>
			) : (
				<label className="flex items-center gap-2 text-muted text-xs">
					<span className="w-16 shrink-0">{t("runner.worktreeName")}</span>
					<input
						className={FIELD}
						value={pick.name}
						placeholder={worktreeSlug(prompt)}
						maxLength={63}
						aria-invalid={error === "worktreeName"}
						onChange={(e) => onPick({ ...pick, name: e.target.value.trim() })}
					/>
				</label>
			)}
			<p className="break-all text-2xs text-faint">
				{pick.mode === "worktree"
					? t("runner.worktreeHint", { dir: runnerPickDir(pick) })
					: runnerPickDir(pick)}
			</p>
			{error && (
				<p role="alert" className="text-2xs text-err">
					{t(
						error === "subpath"
							? "runner.subpathInvalid"
							: "runner.worktreeNameInvalid",
					)}
				</p>
			)}
		</div>
	);
}

function RunnerRow({
	runner,
	expanded,
	onToggle,
	pick,
	prompt,
	onPick,
}: {
	runner: Runner;
	expanded: boolean;
	onToggle: () => void;
	pick: RunnerPick | null;
	prompt: string;
	onPick: (pick: RunnerPick) => void;
}): ReactNode {
	const { t } = useTranslation();
	const online = useRunnerOnline(runner);
	const open = online && expanded;
	const choose = (dir: RunnerDir, mode: RunnerMode) => {
		const same = pick?.runnerId === runner.id && pick.dir.path === dir.path;
		onPick({
			runnerId: runner.id,
			runnerName: runner.name,
			dir,
			sub: same ? pick.sub : "",
			mode,
			name: same ? pick.name : "",
		});
	};
	return (
		<div data-testid="runner-row">
			<button
				type="button"
				disabled={!online}
				aria-expanded={online ? expanded : undefined}
				onClick={onToggle}
				className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-fg text-sm transition-colors duration-150 hover:bg-hover disabled:cursor-default disabled:hover:bg-transparent"
			>
				<span
					aria-hidden
					className="inline-flex w-[18px] shrink-0 justify-center"
				>
					<StatusDot tone={online ? "ok" : "neutral"} />
				</span>
				<span className="flex min-w-0 flex-1 items-baseline gap-2">
					<span
						className={`min-w-0 shrink truncate ${online ? "" : "text-muted"}`}
					>
						{runner.name}
					</span>
					{runner.hostname && (
						<span className="min-w-0 shrink-[2] truncate text-faint text-xs">
							{runner.hostname}
						</span>
					)}
				</span>
				{online ? (
					<Icon
						name={expanded ? "chevronDown" : "chevronRight"}
						size={13}
						className="shrink-0 text-faint"
					/>
				) : (
					<Badge>
						{runner.lastSeenAt
							? t("runner.offline")
							: t("runner.neverConnected")}
					</Badge>
				)}
			</button>
			{open && (
				<div className="pl-[22px]">
					{runner.dirs.length === 0 && (
						<p className="px-2 py-1.5 text-faint text-xs">
							{t("runner.noDirs")}
						</p>
					)}
					{runner.dirs.map((dir) => {
						const selected =
							pick?.runnerId === runner.id && pick.dir.path === dir.path;
						return (
							<div key={dir.path}>
								<div
									className={`flex items-center gap-1 rounded-md pr-1 transition-colors duration-150 ${selected ? "bg-hover" : "hover:bg-hover"}`}
								>
									<button
										type="button"
										aria-pressed={selected}
										onClick={() =>
											choose(dir, selected ? pick.mode : "current")
										}
										className="flex min-w-0 flex-1 items-center gap-2 py-1.5 pl-2 text-left text-sm"
									>
										<Icon
											name={dir.git ? "branch" : "folder"}
											size={14}
											aria-hidden
											className="shrink-0 text-faint"
										/>
										<span title={dir.path} className="min-w-0 flex-1 truncate">
											{dir.name}
										</span>
										{selected && !dir.git && (
											<Icon
												name="check"
												size={13}
												className="shrink-0 text-muted"
											/>
										)}
									</button>
									{dir.git && (
										<ModeToggle
											mode={selected ? pick.mode : "current"}
											selected={selected}
											onMode={(m) => choose(dir, m)}
										/>
									)}
								</div>
								{selected && pick && (
									<DirConfig pick={pick} prompt={prompt} onPick={onPick} />
								)}
							</div>
						);
					})}
				</div>
			)}
		</div>
	);
}

/** 执行位置弹层里的「Runners」分组；没有 runner 时不出现。 */
export function RunnerOptions({
	pick,
	prompt,
	onPick,
}: {
	pick: RunnerPick | null;
	prompt: string;
	onPick: (pick: RunnerPick) => void;
}): ReactNode {
	const { t } = useTranslation();
	const runners = useRunners().data?.runners ?? [];
	// 展开的 runner：默认是已选中的那台；再点一次收起
	const [expanded, setExpanded] = useState<string | null>(
		pick?.runnerId ?? null,
	);
	if (runners.length === 0) return null;
	return (
		<>
			<PopoverLabel>{t("runner.group")}</PopoverLabel>
			{runners.map((r) => (
				<RunnerRow
					key={r.id}
					runner={r}
					expanded={expanded === r.id}
					onToggle={() => setExpanded(expanded === r.id ? null : r.id)}
					pick={pick}
					prompt={prompt}
					onPick={onPick}
				/>
			))}
		</>
	);
}
