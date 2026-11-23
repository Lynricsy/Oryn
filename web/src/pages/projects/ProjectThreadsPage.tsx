/**
 * 项目的线程子页（M24）：本项目全部线程（含已归档），按标题与标签搜索、按状态筛选；搜索词与状态写进 URL
 * （`?q=`、`?status=`），分区记住的地址带着它们，刷新与切分区回来都保持。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import type { Project } from "../../api/client";
import { useNow } from "../../components/layout/pane-parts";
import { Card } from "../../components/ui/data";
import {
	Button,
	EmptyState,
	ErrorText,
	Spinner,
	TextInput,
	tabClass,
} from "../../components/ui/primitives";
import { useDebounced } from "../../lib/use-debounced";
import { NewThreadButton } from "./home/HeaderActions";
import { ProjectThreadRow } from "./home/ProjectThreadRow";
import { ProjectGate, ProjectShell } from "./ProjectShell";
import {
	matchesQuery,
	STATUS_FILTERS,
	type ThreadStatusFilter,
	useProjectThreads,
} from "./project-threads";

const STATUSES: readonly ThreadStatusFilter[] = [
	"all",
	"active",
	"archived",
	"shipped",
];

export function ProjectThreadsPage(): ReactNode {
	return <ProjectGate>{(p) => <ProjectThreads project={p} />}</ProjectGate>;
}

function ProjectThreads({ project }: { project: Project }): ReactNode {
	const { t } = useTranslation();
	const [sp, setSp] = useSearchParams();
	// 非法值按「全部」处理
	const status = STATUSES.find((s) => s === sp.get("status")) ?? "all";
	const urlQ = sp.get("q") ?? "";
	const [text, setText] = useState(urlQ);
	const debounced = useDebounced(text, 200);
	// 最近一次写进 URL 的搜索词：URL 变成别的值才是外部改动（清除筛选、浏览器后退），这时同步回输入框
	const written = useRef(urlQ);
	const setSpRef = useRef(setSp);
	setSpRef.current = setSp;
	useEffect(() => {
		if (urlQ === written.current) return;
		written.current = urlQ;
		setText(urlQ);
	}, [urlQ]);
	useEffect(() => {
		const next = debounced.trim() ? debounced : "";
		if (next === written.current) return;
		written.current = next;
		setSpRef.current(
			(prev) => {
				const n = new URLSearchParams(prev);
				if (next) n.set("q", next);
				else n.delete("q");
				return n;
			},
			{ replace: true },
		);
	}, [debounced]);
	const pickStatus = (s: ThreadStatusFilter) =>
		setSp(
			(prev) => {
				const n = new URLSearchParams(prev);
				if (s === "all") n.delete("status");
				else n.set("status", s);
				return n;
			},
			{ replace: true },
		);

	const now = useNow();
	const { threads, archivedPending, archivedError } = useProjectThreads(
		project.id,
	);
	const shown = threads
		.filter(STATUS_FILTERS[status])
		.filter((th) => matchesQuery(th, urlQ));
	const loadingArchived = archivedPending && status !== "active";

	return (
		<ProjectShell
			project={project}
			wide
			crumbs={[{ label: t("projectThreads.title") }]}
			actions={<NewThreadButton project={project} />}
		>
			<div data-testid="project-threads" className="flex flex-col gap-3">
				<div className="flex flex-wrap items-center gap-2">
					<div className="min-w-0 flex-1 basis-48">
						<TextInput
							type="search"
							aria-label={t("projectThreads.search")}
							placeholder={t("projectThreads.search")}
							value={text}
							onChange={(e) => setText(e.target.value)}
						/>
					</div>
					{/* biome-ignore lint/a11y/useSemanticElements: 分段按钮（aria-pressed），不是表单字段集 */}
					<div
						role="group"
						aria-label={t("projectThreads.statusLabel")}
						className="flex shrink-0 items-center gap-0.5 rounded-full bg-inset p-0.5"
					>
						{STATUSES.map((s) => (
							<button
								key={s}
								type="button"
								aria-pressed={status === s}
								onClick={() => pickStatus(s)}
								className={`${tabClass(status === s)} h-6 ${status === s ? "bg-panel shadow-xs" : ""}`}
							>
								{t(`projectThreads.status.${s}`)}
							</button>
						))}
					</div>
				</div>
				<ErrorText error={archivedError} />
				{shown.length > 0 ? (
					<Card>
						<ul className="-mx-2 p-2">
							{shown.map((th) => (
								<ProjectThreadRow key={th.id} thread={th} now={now} />
							))}
						</ul>
					</Card>
				) : loadingArchived ? null : threads.length === 0 ? (
					<EmptyState icon="chats" title={t("projectHome.noThreads")} />
				) : (
					<EmptyState icon="search" title={t("projectThreads.noMatch")}>
						<Button
							size="sm"
							onClick={() => setSp(new URLSearchParams(), { replace: true })}
						>
							{t("projectThreads.clear")}
						</Button>
					</EmptyState>
				)}
				{loadingArchived && (
					<div className="flex justify-center p-3 text-muted">
						<Spinner size={16} />
					</div>
				)}
			</div>
		</ProjectShell>
	);
}
