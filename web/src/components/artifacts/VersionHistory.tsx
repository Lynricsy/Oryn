/**
 * 版本历史（计划 M14 2.6、M29 I1）：查看器侧栏列里的版本列表，从新到旧。每行：`v{n}`、主标题、小字（作者 + 相对时间）、
 * 设计检查徽标（有 `check` 才显示），最新一行带「最新」标签；选中的一行高亮。点一行 → `onSelect`（查看器以该版本只读显示）。
 * 标题与关闭按钮在查看器侧栏的页头（与评论面板共用），这里只有列表。
 * 主标题：`title`（回合标题）→ `note` → 作者名。
 * 按回合折叠：`turnId` 相同且相邻的版本合成一组，组头显示版本区间（新–旧）、组内最新版本的主标题、作者与时间、检查徽标，
 * 点组头展开 / 收起；组内各行照单行样式，主标题不再重复回合标题（`note` → 作者名）。只有一个版本的回合与没有 `turnId`
 * 的版本都是单行。选中的版本（不是最新时）所在的组自动展开；组收起时选中落在组内则组头高亮。
 * 作者名：没有作者线程 = 「你」；线程还在显示它当前的标题；不在了显示写入时的标题快照，再没有显示「已删除的线程」。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type ArtifactDetail,
	type ArtifactVersion,
	useArtifactVersions,
} from "../../api/artifact-queries";
import { formatRelative } from "../../lib/format";
import { useIndex } from "../../state/stores";
import { Badge, type Tone } from "../ui/data";
import { Icon, type IconName } from "../ui/icons";
import { Button, describeError, Spinner } from "../ui/primitives";

type DetailThread = ArtifactDetail["threads"][number];
type Check = NonNullable<ArtifactVersion["check"]>;

/** 列表的一项：单个版本，或同一回合的相邻版本（≥ 2 个，新 → 旧）。 */
export type HistoryEntry =
	| { kind: "version"; version: ArtifactVersion }
	| { kind: "turn"; key: string; turnId: string; versions: ArtifactVersion[] };

/** 版本作者的显示名（规则见文件头）。`liveTitle` 查当前还在的线程标题。 */
export function authorName(
	v: Pick<ArtifactVersion, "authorThreadId" | "authorLabel">,
	liveTitle: (threadId: string) => string | undefined,
	t: TFunction,
): string {
	if (!v.authorThreadId) return t("artifacts.history.you");
	return (
		liveTitle(v.authorThreadId) ||
		v.authorLabel ||
		t("artifacts.history.deletedThread")
	);
}

/** 把新 → 旧的版本按回合折叠：`turnId` 相同且相邻的合成一组；不相邻的同一回合各自成组。 */
export function groupByTurn(list: readonly ArtifactVersion[]): HistoryEntry[] {
	const out: HistoryEntry[] = [];
	let run: ArtifactVersion[] = [];
	const flush = () => {
		const [first] = run;
		const last = run.at(-1);
		if (!first || !last) return;
		if (run.length === 1 || !first.turnId)
			out.push({ kind: "version", version: first });
		else
			out.push({
				kind: "turn",
				// 用组内最旧的版本作 key：同一回合再来新版本时展开状态不丢
				key: `${first.turnId}:${last.version}`,
				turnId: first.turnId,
				versions: run,
			});
		run = [];
	};
	for (const v of list) {
		if (!v.turnId || run[0]?.turnId !== v.turnId) flush();
		run.push(v);
		if (!v.turnId) flush();
	}
	flush();
	return out;
}

const CHECK_LOOK: Record<Check["status"], { tone: Tone; icon: IconName }> = {
	clean: { tone: "ok", icon: "checkCircle" },
	issues: { tone: "warn", icon: "alert" },
	stale: { tone: "neutral", icon: "clock" },
	unverified: { tone: "neutral", icon: "question" },
};

/** 设计检查徽标；没有检查结果不显示。 */
function CheckBadge({ check }: { check: ArtifactVersion["check"] }): ReactNode {
	const { t } = useTranslation();
	if (!check) return null;
	const look = CHECK_LOOK[check.status];
	const text =
		check.status === "issues"
			? t("artifacts.history.check.issues", { count: check.issues.length })
			: t(`artifacts.history.check.${check.status}`);
	return (
		<span data-testid="version-check" data-status={check.status}>
			<Badge tone={look.tone} icon={look.icon}>
				{text}
			</Badge>
		</span>
	);
}

export function VersionHistory({
	artifactId,
	head,
	threads,
	selected,
	onSelect,
}: {
	artifactId: string;
	head: number;
	/** 详情里还在的相关线程（创建者与写过版本的）。 */
	threads: readonly DetailThread[];
	/** 正在看的版本号（跟随最新时 = head）。 */
	selected: number;
	onSelect(v: ArtifactVersion): void;
}): ReactNode {
	const { t } = useTranslation();
	const versions = useArtifactVersions(artifactId);
	const index = useIndex((s) => s.threads);
	const liveTitle = (id: string) =>
		threads.find((x) => x.threadId === id)?.title ?? index[id]?.title;
	// 服务端已按新 → 旧返回；这里再排一次，不依赖顺序
	const list = (versions.data ?? []).toSorted((a, b) => b.version - a.version);
	const entries = groupByTurn(list);
	const holding = (e: HistoryEntry) =>
		e.kind === "turn" && e.versions.some((v) => v.version === selected);

	// 用户点组头的展开 / 收起（按组 key）；没记过的组：选中的旧版本在组内就展开（进来时就钉着旧版本）
	const [toggled, setToggled] = useState<ReadonlyMap<string, boolean>>(
		new Map(),
	);
	// 选中换了：它所在的组记为展开（含点组内的最新版本回到跟随最新时）
	const [seen, setSeen] = useState(selected);
	if (seen !== selected) {
		setSeen(selected);
		const g = entries.find(holding);
		if (g?.kind === "turn" && toggled.get(g.key) !== true)
			setToggled(new Map(toggled).set(g.key, true));
	}
	const isOpen = (e: HistoryEntry & { kind: "turn" }) =>
		toggled.get(e.key) ?? (selected !== head && holding(e));

	const row = (v: ArtifactVersion, inTurn: boolean) => {
		const author = authorName(v, liveTitle, t);
		const current = v.version === selected;
		return (
			<li key={v.version}>
				<button
					type="button"
					data-version={v.version}
					aria-current={current ? "true" : undefined}
					onClick={() => onSelect(v)}
					className={`flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors duration-150 hover:bg-hover ${current ? "bg-active" : ""}`}
				>
					<span className="mt-px w-8 shrink-0 font-mono text-muted text-xs tabular-nums">
						{t("artifacts.viewer.version", { n: v.version })}
					</span>
					<span className="flex min-w-0 flex-1 flex-col gap-0.5">
						<span className="flex min-w-0 items-center gap-1.5">
							<span className="min-w-0 truncate font-medium text-fg text-sm">
								{(inTurn ? "" : v.title) || v.note || author}
							</span>
							{v.version === head && (
								<Badge tone="neutral">{t("artifacts.history.latest")}</Badge>
							)}
							<CheckBadge check={v.check} />
						</span>
						<span className="truncate text-faint text-xs">
							{t("artifacts.history.byline", {
								author,
								when: formatRelative(v.createdAt),
							})}
						</span>
					</span>
				</button>
			</li>
		);
	};

	const group = (e: HistoryEntry & { kind: "turn" }) => {
		const [top] = e.versions;
		const bottom = e.versions.at(-1);
		if (!top || !bottom) return null;
		const open = isOpen(e);
		const author = authorName(top, liveTitle, t);
		// 收起时选中落在组内：组头代为高亮
		const current = !open && holding(e);
		return (
			<li key={e.key} data-testid="version-group" data-turn={e.turnId}>
				<button
					type="button"
					aria-expanded={open}
					onClick={() => setToggled((m) => new Map(m).set(e.key, !isOpen(e)))}
					className={`flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors duration-150 hover:bg-hover ${current ? "bg-active" : ""}`}
				>
					<span className="mt-px flex shrink-0 items-center gap-1 font-mono text-muted text-xs tabular-nums">
						<Icon
							name="chevronRight"
							size={12}
							className={`shrink-0 text-faint transition-transform duration-150 ${open ? "rotate-90" : ""}`}
						/>
						{t("artifacts.history.range", {
							from: top.version,
							to: bottom.version,
						})}
					</span>
					<span className="flex min-w-0 flex-1 flex-col gap-0.5">
						<span className="flex min-w-0 items-center gap-1.5">
							<span className="min-w-0 truncate font-medium text-fg text-sm">
								{top.title || top.note || author}
							</span>
							{!open && top.version === head && (
								<Badge tone="neutral">{t("artifacts.history.latest")}</Badge>
							)}
							<CheckBadge check={top.check} />
						</span>
						<span className="truncate text-faint text-xs">
							{t("artifacts.history.byline", {
								author,
								when: formatRelative(top.createdAt),
							})}
						</span>
					</span>
				</button>
				{open && (
					<ol
						aria-label={t("artifacts.history.turnVersions", {
							from: top.version,
							to: bottom.version,
						})}
						className="mt-0.5 ml-4 flex flex-col gap-0.5 border-border border-l pl-1"
					>
						{e.versions.map((v) => row(v, true))}
					</ol>
				)}
			</li>
		);
	};

	let body: ReactNode;
	if (versions.isPending)
		body = (
			<div className="flex justify-center py-8 text-faint">
				<Spinner />
			</div>
		);
	else if (versions.isError)
		body = (
			<div className="flex flex-col items-center gap-3 px-4 py-8 text-center text-muted text-sm">
				<span className="break-words">{describeError(versions.error, t)}</span>
				<Button
					size="sm"
					icon="refresh"
					onClick={() => void versions.refetch()}
				>
					{t("artifacts.viewer.retry")}
				</Button>
			</div>
		);
	else
		body = (
			<ol
				aria-label={t("artifacts.history.title")}
				data-testid="version-list"
				className="flex flex-col gap-0.5 p-1.5"
			>
				{entries.map((e) =>
					e.kind === "version" ? row(e.version, false) : group(e),
				)}
			</ol>
		);

	return <div data-testid="version-history">{body}</div>;
}
