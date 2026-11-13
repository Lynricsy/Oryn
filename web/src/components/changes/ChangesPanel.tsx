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
	type FormEvent,
	type ReactNode,
	useCallback,
	useEffect,
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
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Checkbox,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../ui/primitives";
import { CommitPicker } from "./CommitPicker";
import { FileDiff } from "./FileDiff";
import {
	type ChangeScope,
	changesRootKey,
	type GitCommit,
	type GitFileChange,
	type GitStatus,
	type GitTarget,
	getGitStatus,
	isAbort,
	isValidBranch,
	listGitCommits,
	readChangesRoot,
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

/** 状态字母方块的底色与字色（语义色 12–15% 底）。 */
const STATUS_TONE: Record<GitFileChange["status"], string> = {
	added: "bg-ok/12 text-ok-ink",
	modified: "bg-warn/15 text-warn",
	deleted: "bg-err/10 text-err",
	conflicted: "bg-err/10 text-err",
	renamed: "bg-frost/12 text-frost",
	copied: "bg-frost/12 text-frost",
	typechange: "bg-inset text-muted",
	untracked: "bg-inset text-muted",
};

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

/** 「与分支比较…」弹出的分支名输入；提交前按服务端的分支名规则校验。 */
function BaseBranchForm({
	initial,
	custom,
	onApply,
	onReset,
	onClose,
}: {
	initial: string;
	/** 当前是否为手动指定的基线（是则提供「恢复默认基线」） */
	custom: boolean;
	onApply(name: string): void;
	onReset(): void;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const [name, setName] = useState(initial);
	const [invalid, setInvalid] = useState(false);
	const submit = (e: FormEvent) => {
		e.preventDefault();
		const v = name.trim();
		if (!isValidBranch(v)) {
			setInvalid(true);
			return;
		}
		onApply(v);
	};
	return (
		<form
			onSubmit={submit}
			onKeyDown={(e) => {
				if (e.key === "Escape") {
					e.stopPropagation();
					onClose();
				}
			}}
			className="pop-in flex flex-col gap-1.5 rounded-lg border border-border bg-raised p-2 shadow-xs"
		>
			<div className="flex items-center gap-1.5">
				<Icon
					name="branch"
					size={14}
					aria-hidden
					className="shrink-0 text-faint"
				/>
				<TextInput
					autoFocus
					aria-label={t("changes.base.label")}
					aria-invalid={invalid || undefined}
					placeholder={t("changes.base.placeholder")}
					spellCheck={false}
					autoCapitalize="off"
					autoCorrect="off"
					value={name}
					onChange={(e) => {
						setName(e.target.value);
						setInvalid(false);
					}}
					className="h-7 min-w-0 flex-1 py-0 font-mono text-xs"
				/>
			</div>
			{invalid && (
				<p className="text-2xs text-err">{t("changes.base.invalid")}</p>
			)}
			<div className="flex items-center gap-1">
				{custom && (
					<Button type="button" size="sm" variant="ghost" onClick={onReset}>
						{t("changes.base.reset")}
					</Button>
				)}
				<span className="flex-1" />
				<Button type="button" size="sm" variant="ghost" onClick={onClose}>
					{t("common.cancel")}
				</Button>
				<Button
					type="submit"
					size="sm"
					variant="primary"
					disabled={name.trim() === ""}
				>
					{t("changes.base.apply")}
				</Button>
			</div>
		</form>
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
		readChangesRoot(threadId),
	);
	// 手动指定的基线分支（「与分支比较…」）；缺省由服务端推定
	const [base, setBase] = useState<string | undefined>(undefined);
	const [basePrompt, setBasePrompt] = useState(false);
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
		() => (orbId ? { orbId, root, base, scope } : null),
		[orbId, root, base, scope],
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
					base,
					bg,
					lifetime.current?.signal,
				);
				if (n !== commitSeq.current) return;
				setCommits({ list: r.commits, truncated: r.truncated });
			} catch {
				// 列表只是选择器的辅助数据；失败时保留旧值，错误由 status 呈现
			}
		},
		[orbId, root, base],
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
			if (r) localStorage.setItem(changesRootKey(threadId), r);
			else localStorage.removeItem(changesRootKey(threadId));
		} catch {
			// 不可写时只保留在内存
		}
		setSelected(null);
		setStatus(null);
		// 分支名属于原来的仓库，换仓库后回到服务端推定的基线
		setBase(undefined);
		setBasePrompt(false);
		setRootState(r);
	};

	/** 换基线（`undefined` = 恢复默认）：回到「全部改动」范围查看相对它的改动。 */
	const applyBase = (b: string | undefined) => {
		setBasePrompt(false);
		setSelected(null);
		setScope({ scope: "all" });
		setBase(b);
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
	const modified = files.filter((f) => f.status === "modified").length;
	const linkClass =
		"rounded-xs text-2xs text-muted underline-offset-2 hover:text-fg hover:underline";

	const header = (
		<div className="flex shrink-0 flex-col gap-1.5 border-border border-b px-2 py-2">
			<div className="flex items-center gap-1">
				<CommitPicker
					value={scope}
					onChange={(s) => {
						setSelected(null);
						setScope(s);
					}}
					onCompareBranch={() => setBasePrompt(true)}
					baseName={status?.base?.name ?? base}
					commits={commits.list}
					truncated={commits.truncated}
				/>
				<IconButton
					icon="refresh"
					label={t("changes.refresh")}
					onClick={() => {
						void load(false);
						void loadCommits();
					}}
				/>
			</div>
			{basePrompt && (
				<BaseBranchForm
					initial={base ?? status?.base?.name ?? ""}
					custom={base !== undefined}
					onApply={applyBase}
					onReset={() => applyBase(undefined)}
					onClose={() => setBasePrompt(false)}
				/>
			)}
			{status?.repo && (
				// 摘要在左、动作在右：Review / Ship ▾ / Sync ▾ 动作簇（见设计文档「未来功能落位」）
				// 将作为本行的第二个子元素靠右排列，左侧摘要随之收窄换行
				<div className="flex items-center gap-2">
					<div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1 px-0.5 text-faint text-xs">
						{status.branch && (
							<Badge icon="branch" title={status.branch}>
								<span className="font-mono">{status.branch}</span>
							</Badge>
						)}
						{status.base?.approximate && scope.scope === "all" && (
							<Badge tone="warn">{t("changes.approximateBase")}</Badge>
						)}
						{scope.scope === "all" && !status.base && (
							<span>{t("changes.noBase")}</span>
						)}
						<span className="inline-flex items-center gap-1.5 tabular-nums">
							<span>{t("changes.summary", { n: files.length })}</span>
							{modified > 0 && (
								// 「~3」在小号等宽字下像「-3」：改用铅笔图标 + 数字，读屏读整句
								<span
									className="inline-flex items-center gap-0.5 text-warn"
									title={t("changes.modified", { n: modified })}
								>
									<Icon name="edit" size={11} aria-hidden />
									<span aria-hidden>{modified}</span>
									<span className="sr-only">
										{t("changes.modified", { n: modified })}
									</span>
								</span>
							)}
							<span className="text-ok">{`+${additions}`}</span>
							<span className="text-err">{`−${deletions}`}</span>
						</span>
						{base !== undefined && (
							<button
								type="button"
								className={linkClass}
								onClick={() => applyBase(undefined)}
							>
								{t("changes.base.reset")}
							</button>
						)}
						{root && (
							<button
								type="button"
								className={linkClass}
								onClick={() => setRoot(undefined)}
							>
								{t("changes.resetRoot")}
							</button>
						)}
					</div>
				</div>
			)}
			{stageable && files.length > 0 && (
				<div className="-mx-1 flex items-center gap-1">
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
				<EmptyState icon="branch" title={t("changes.notRepo")} />
				{(status.candidates?.length ?? 0) > 0 && (
					<div className="flex flex-col gap-1">
						<p className="text-muted text-xs">{t("changes.pickRepo")}</p>
						{status.candidates?.map((c) => (
							<Button
								key={c}
								size="sm"
								variant="secondary"
								icon="folder"
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
		list = <EmptyState icon="checkCircle" title={t("changes.clean")} />;
	} else {
		list = (
			<ul aria-label={t("changes.files")} className="p-1.5 text-sm">
				{groups.map(([dir, group]) => (
					<li key={dir}>
						<div className="flex items-center gap-1.5 px-2 pt-2 pb-1 text-2xs text-faint">
							<Icon name="folder" size={12} aria-hidden className="shrink-0" />
							<span className="min-w-0 truncate font-mono">
								{dir || t("changes.rootDir")}
							</span>
						</div>
						<ul>
							{group.map((f) => (
								<li key={f.path}>
									<div
										className={`flex h-8 items-center gap-2 rounded-md px-2 transition-colors duration-150 ${selected === f.path ? "bg-hover text-fg" : "hover:bg-hover/60"}`}
									>
										{stageable && (
											<Checkbox
												aria-label={t("changes.stageFile", { path: f.path })}
												checked={f.staged === "full"}
												indeterminate={f.staged === "partial"}
												data-staged={f.staged}
												disabled={staging}
												onChange={() => void stage([f], f.staged !== "full")}
											/>
										)}
										{/* 三个直接子 span 依次为状态字母、名称、统计（验收脚本按此读取） */}
										<button
											type="button"
											aria-current={selected === f.path}
											className="flex h-full min-w-0 flex-1 items-center gap-2 rounded-sm text-left"
											onClick={() => setSelected(f.path)}
										>
											<span
												className={`inline-grid h-[18px] w-[18px] shrink-0 place-items-center rounded font-mono font-semibold text-2xs ${STATUS_TONE[f.status]}`}
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
												<span className="inline-flex h-[18px] shrink-0 items-center rounded bg-inset px-1.5 font-medium text-2xs text-muted">
													{t("changes.binary")}
												</span>
											) : (
												<span className="shrink-0 font-mono text-2xs tabular-nums">
													{f.additions !== undefined && (
														<span className="text-ok">{`+${f.additions}`}</span>
													)}
													{f.deletions !== undefined && (
														<span className="ml-1.5 text-err">{`−${f.deletions}`}</span>
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
					<li className="px-2 py-1.5 text-warn text-xs">
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
			<div className="flex h-full items-center justify-center p-6">
				<EmptyState icon="diff" title={t("changes.pick")} />
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
