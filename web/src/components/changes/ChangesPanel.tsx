/**
 * 线程右侧面板的 Changes 标签：相对基线的文件列表与 diff、文件级暂存、按 commit 查看。
 * 刷新：打开、手动刷新、暂存操作之后、agent 活动（工具结束 / 回合结束 / 终端退出）后 1s 防抖；
 * 另有每 10s 一次的 `bg=1` 轮询，仅在文档可见且 orb 为 `ready` 时进行（不唤醒、不计用户交互）。
 * 每次刷新成功后都重拉已打开文件的 diff（后台刷新同样以 bg 发出）：增删行数不变时内容也可能已变。
 * 单个 commit 的 diff 不会变，后台刷新不重拉。commit 列表请求按序号校验，换目标后旧结果作废。
 * 已有请求在途时跳过后台刷新（慢 orb 上不堆积）；每个请求 30s 超时，面板卸载即取消，
 * 后台请求的取消 / 超时不报错。
 * 面板卸载即停止请求。不做 hunk 级暂存、提交与丢弃。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../api/client";
import { useThread } from "../../state/stores";
import {
	isOrbdOutdated,
	useElementWidth,
	useWorkspaceActivity,
} from "../files/hooks";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
} from "../ui/primitives";
import { CommitPicker } from "./CommitPicker";
import { FileDiff } from "./FileDiff";
import {
	type ChangeScope,
	type GitCommit,
	type GitFileChange,
	type GitStatus,
	type GitTarget,
	getGitStatus,
	isAbort,
	listGitCommits,
	setStaged,
} from "./git";

/** 后台轮询间隔。 */
export const POLL_MS = 10_000;
const SPLIT_MIN_WIDTH = 720;

const STATUS_LETTER: Record<GitFileChange["status"], string> = {
	added: "A",
	modified: "M",
	deleted: "D",
	renamed: "R",
	copied: "C",
	typechange: "T",
	untracked: "U",
	conflicted: "!",
};

const STATUS_TONE: Record<GitFileChange["status"], string> = {
	added: "text-ok",
	untracked: "text-ok",
	modified: "text-warn",
	typechange: "text-warn",
	renamed: "text-accent",
	copied: "text-accent",
	deleted: "text-err",
	conflicted: "text-err",
};

function rootKey(threadId: string) {
	return `oryn.changes.root.${threadId}`;
}

function readRoot(threadId: string): string | undefined {
	try {
		return localStorage.getItem(rootKey(threadId)) ?? undefined;
	} catch {
		return undefined;
	}
}

function dirOf(path: string): string {
	const i = path.lastIndexOf("/");
	return i < 0 ? "" : path.slice(0, i);
}

/** 按目录分组（保持服务端顺序内的目录首次出现顺序，组内按路径排序）。 */
export function groupByDir(
	files: GitFileChange[],
): [string, GitFileChange[]][] {
	const groups = new Map<string, GitFileChange[]>();
	for (const f of [...files].sort((a, b) => a.path.localeCompare(b.path))) {
		const d = dirOf(f.path);
		const list = groups.get(d);
		if (list) list.push(f);
		else groups.set(d, [f]);
	}
	return [...groups.entries()];
}

function StageBox({
	file,
	disabled,
	onToggle,
}: {
	file: GitFileChange;
	disabled: boolean;
	onToggle(): void;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLInputElement>(null);
	// 绘制前同步 DOM 属性：避免先画出非半选态再跳变
	useLayoutEffect(() => {
		if (ref.current) ref.current.indeterminate = file.staged === "partial";
	}, [file.staged]);
	return (
		<input
			ref={ref}
			type="checkbox"
			aria-label={t("changes.stageFile", { path: file.path })}
			checked={file.staged === "full"}
			data-staged={file.staged}
			disabled={disabled}
			onChange={onToggle}
			className="shrink-0"
		/>
	);
}

export default function ChangesPanel({
	threadId,
	orbId,
}: {
	threadId: string;
	orbId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const orbState = useThread(threadId, (s) => s.orb?.state);
	const [root, setRootState] = useState<string | undefined>(() =>
		readRoot(threadId),
	);
	const [scope, setScope] = useState<ChangeScope>({ scope: "all" });
	const [status, setStatus] = useState<GitStatus | null>(null);
	const [error, setError] = useState<unknown>(null);
	const [commits, setCommits] = useState<{
		list: GitCommit[];
		truncated: boolean;
	}>({
		list: [],
		truncated: false,
	});
	const [selected, setSelected] = useState<string | null>(null);
	const [staging, setStaging] = useState(false);
	const [refresh, setRefresh] = useState({ n: 0, bg: false });
	const hostRef = useRef<HTMLDivElement>(null);
	const split = useElementWidth(hostRef, SPLIT_MIN_WIDTH) >= SPLIT_MIN_WIDTH;

	const target = useMemo<GitTarget | null>(
		() => (orbId ? { orbId, root, scope } : null),
		[orbId, root, scope],
	);
	const seq = useRef(0);
	const commitSeq = useRef(0);
	// 在途的状态请求数：大于 0 时后台刷新直接跳过
	const inflight = useRef(0);
	// 面板卸载时取消全部请求
	const lifetime = useRef<AbortController | null>(null);
	useEffect(() => {
		const ctl = new AbortController();
		lifetime.current = ctl;
		return () => ctl.abort();
	}, []);
	const headRef = useRef<string | undefined>(undefined);

	const loadCommits = useCallback(
		async (bg = false) => {
			const n = ++commitSeq.current;
			if (!orbId) return;
			try {
				const r = await listGitCommits(
					orbId,
					root,
					bg,
					lifetime.current?.signal,
				);
				if (n !== commitSeq.current) return;
				setCommits({ list: r.commits, truncated: r.truncated });
			} catch {
				// 列表只是选择器的辅助数据；失败时保留旧值，错误由 status 呈现
			}
		},
		[orbId, root],
	);

	const load = useCallback(
		async (bg: boolean) => {
			if (!target) return;
			if (bg && inflight.current > 0) return;
			const n = ++seq.current;
			inflight.current++;
			try {
				const s = await getGitStatus(target, bg, lifetime.current?.signal);
				if (n !== seq.current) return;
				setStatus(s);
				setError(null);
				// HEAD 变化（agent 提交了新 commit）时刷新 commit 列表
				if (s.repo && s.head !== headRef.current) {
					const first = headRef.current === undefined;
					headRef.current = s.head;
					if (!first) void loadCommits(bg);
				}
				// 重拉已打开文件的 diff：状态元数据（含增删行数）不变不代表内容未变；
				// 单个 commit 的内容固定，后台刷新时跳过
				if (!bg || target.scope.scope !== "commit")
					setRefresh((r) => ({ n: r.n + 1, bg }));
			} catch (e) {
				if (n !== seq.current) return;
				// 后台请求遇到 orb 不可用 / 取消 / 超时：保留已有数据，等下一次
				if (bg && e instanceof ApiError && e.code === "orb_not_ready") return;
				if (bg && isAbort(e)) return;
				if (lifetime.current?.signal.aborted) return;
				setError(e);
			} finally {
				inflight.current--;
			}
		},
		[target, loadCommits],
	);

	// 打开 / 换范围 / 换仓库目录：前台加载（必要时唤醒 orb）
	useEffect(() => {
		void load(false);
	}, [load]);

	// 打开 / 换仓库目录：拉 commit 列表
	useEffect(() => {
		headRef.current = undefined;
		void loadCommits();
	}, [loadCommits]);

	// 后台轮询
	const pollable = orbState === "ready";
	useEffect(() => {
		if (!pollable) return;
		const timer = setInterval(() => {
			if (document.visibilityState === "visible") void load(true);
		}, POLL_MS);
		return () => clearInterval(timer);
	}, [pollable, load]);

	useWorkspaceActivity(orbId ? threadId : undefined, () => void load(true), {
		terminal: true,
	});

	const setRoot = (r: string | undefined) => {
		try {
			if (r) localStorage.setItem(rootKey(threadId), r);
			else localStorage.removeItem(rootKey(threadId));
		} catch {
			// 不可写时只保留在内存
		}
		setSelected(null);
		setStatus(null);
		setRootState(r);
	};

	const stage = async (files: GitFileChange[], on: boolean) => {
		if (!orbId) return;
		setStaging(true);
		try {
			await setStaged(orbId, status?.root ?? root, files, on);
			await load(false);
		} catch (e) {
			setError(e);
		} finally {
			setStaging(false);
		}
	};

	if (!orbId)
		return <EmptyState icon="alert" title={t("changes.orbDeleted")} />;
	if (isOrbdOutdated(error))
		return <EmptyState icon="alert" title={t("changes.outdated")} />;

	const files = status?.repo ? status.files : [];
	const stageable = scope.scope !== "commit";
	const selectedFile = files.find((f) => f.path === selected) ?? null;
	const groups = groupByDir(files);
	const additions = files.reduce((n, f) => n + (f.additions ?? 0), 0);
	const deletions = files.reduce((n, f) => n + (f.deletions ?? 0), 0);

	const header = (
		<div className="flex shrink-0 flex-col gap-1 border-border border-b px-2 py-1.5">
			<div className="flex items-center gap-1">
				<CommitPicker
					value={scope}
					onChange={(s) => {
						setSelected(null);
						setScope(s);
					}}
					baseName={status?.base?.name}
					commits={commits.list}
					truncated={commits.truncated}
				/>
				<IconButton
					icon="clock"
					label={t("changes.refresh")}
					onClick={() => {
						void load(false);
						void loadCommits();
					}}
				/>
			</div>
			{status?.repo && (
				<div className="flex flex-wrap items-center gap-x-2 text-faint text-xs">
					{status.branch && <span className="font-mono">{status.branch}</span>}
					{status.base?.approximate && scope.scope === "all" && (
						<span className="text-warn">{t("changes.approximateBase")}</span>
					)}
					{scope.scope === "all" && !status.base && (
						<span>{t("changes.noBase")}</span>
					)}
					<span>
						{t("changes.summary", { n: files.length })}
						<span className="ml-1 text-ok">{`+${additions}`}</span>
						<span className="ml-1 text-err">{`−${deletions}`}</span>
					</span>
					{root && (
						<button
							type="button"
							className="underline hover:text-fg"
							onClick={() => setRoot(undefined)}
						>
							{t("changes.resetRoot")}
						</button>
					)}
				</div>
			)}
			{stageable && files.length > 0 && (
				<div className="flex gap-1">
					<Button
						size="sm"
						variant="ghost"
						disabled={staging || files.every((f) => f.staged === "full")}
						onClick={() =>
							void stage(
								files.filter((f) => f.staged !== "full"),
								true,
							)
						}
					>
						{t("changes.stageAll")}
					</Button>
					<Button
						size="sm"
						variant="ghost"
						disabled={staging || files.every((f) => f.staged === "none")}
						onClick={() =>
							void stage(
								files.filter((f) => f.staged !== "none"),
								false,
							)
						}
					>
						{t("changes.unstageAll")}
					</Button>
				</div>
			)}
		</div>
	);

	let list: ReactNode;
	if (!status) {
		list = error ? (
			<div className="p-3">
				<ErrorText error={error} />
			</div>
		) : (
			<div className="p-3">
				<Spinner />
			</div>
		);
	} else if (!status.repo) {
		list = (
			<div className="p-3 text-sm">
				<EmptyState icon="fork" title={t("changes.notRepo")} />
				{(status.candidates?.length ?? 0) > 0 && (
					<div className="flex flex-col gap-1">
						<p className="text-muted text-xs">{t("changes.pickRepo")}</p>
						{status.candidates?.map((c) => (
							<Button
								key={c}
								size="sm"
								className="justify-start font-mono"
								onClick={() => setRoot(c)}
							>
								{c}
							</Button>
						))}
					</div>
				)}
				{root && (
					<Button
						size="sm"
						variant="ghost"
						className="mt-2"
						onClick={() => setRoot(undefined)}
					>
						{t("changes.resetRoot")}
					</Button>
				)}
			</div>
		);
	} else if (files.length === 0) {
		list = <EmptyState icon="check" title={t("changes.clean")} />;
	} else {
		list = (
			<ul aria-label={t("changes.files")} className="p-1 text-sm">
				{groups.map(([dir, group]) => (
					<li key={dir}>
						<div className="truncate px-2 pt-2 pb-0.5 font-mono text-2xs text-faint">
							{dir || t("changes.rootDir")}
						</div>
						<ul>
							{group.map((f) => (
								<li key={f.path}>
									<div
										className={`flex h-7 items-center gap-2 rounded px-2 hover:bg-hover ${selected === f.path ? "bg-hover" : ""}`}
									>
										{stageable && (
											<StageBox
												file={f}
												disabled={staging}
												onToggle={() => void stage([f], f.staged !== "full")}
											/>
										)}
										<button
											type="button"
											aria-current={selected === f.path}
											className="flex h-full min-w-0 flex-1 items-center gap-2 text-left"
											onClick={() => setSelected(f.path)}
										>
											<span
												className={`w-3 shrink-0 text-center font-mono font-semibold text-xs ${STATUS_TONE[f.status]}`}
												title={t(`changes.status.${f.status}`)}
											>
												{STATUS_LETTER[f.status]}
											</span>
											<span
												className="min-w-0 flex-1 truncate font-mono text-xs"
												title={f.path}
											>
												{f.oldPath
													? `${f.oldPath} → ${f.path}`
													: f.path.slice(dir ? dir.length + 1 : 0)}
											</span>
											{f.binary ? (
												<span className="shrink-0 text-2xs text-faint">
													{t("changes.binary")}
												</span>
											) : (
												<span className="shrink-0 font-mono text-2xs">
													{f.additions !== undefined && (
														<span className="text-ok">{`+${f.additions}`}</span>
													)}
													{f.deletions !== undefined && (
														<span className="ml-1 text-err">{`−${f.deletions}`}</span>
													)}
												</span>
											)}
										</button>
									</div>
								</li>
							))}
						</ul>
					</li>
				))}
				{status.truncated && (
					<li className="px-2 py-1 text-warn text-xs">
						{t("changes.truncated")}
					</li>
				)}
			</ul>
		);
	}

	const diff =
		target && selectedFile ? (
			<FileDiff
				target={{ ...target, root: status?.root ?? root }}
				file={selectedFile}
				refresh={refresh}
				onBack={split ? undefined : () => setSelected(null)}
			/>
		) : (
			<div className="flex h-full items-center justify-center p-6 text-faint text-sm">
				{t("changes.pick")}
			</div>
		);

	return (
		<div ref={hostRef} className="flex h-full min-h-0 flex-col">
			{header}
			{status && error ? (
				<div className="shrink-0 px-3 py-1">
					<ErrorText error={error} />
				</div>
			) : null}
			{split ? (
				<div className="flex min-h-0 flex-1">
					<div className="w-80 shrink-0 overflow-y-auto border-border border-r">
						{list}
					</div>
					<div className="min-w-0 flex-1">{diff}</div>
				</div>
			) : selectedFile ? (
				<div className="min-h-0 flex-1">{diff}</div>
			) : (
				<div className="min-h-0 flex-1 overflow-y-auto">{list}</div>
			)}
		</div>
	);
}
