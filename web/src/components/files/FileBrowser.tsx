/**
 * 通用文件浏览器：根切换器 + 文件树 + 预览 / 编辑 + 上传队列。
 * 线程 Files 面板（orb + agent 配置）与设置页的 agent 配置共用。
 * 容器宽 ≥720px 时左树右内容，否则列表 → 详情两级。
 * 刷新时机：展开目录、手动刷新、自己的变更之后、`refreshKey` 变化（由上层在 agent 活动后防抖触发）。
 * 来源不可写（`caps.write` 为 false，如停机 orb 的只读预览）时隐藏一切变更入口，文件以只读方式打开。
 * 目录请求按「来源 + 根」的代次与每个目录的请求序号校验，过期的结果（旧 orb、同目录更早的请求）一律丢弃。
 * 未保存的草稿在 `drafts` store 里，不随查看器卸载丢失；树上以圆点标出。
 */
import {
	Fragment,
	type ReactNode,
	useCallback,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { copyToClipboard } from "../../lib/clipboard";
import {
	baseName,
	type FileEntry,
	type FileSource,
	isDirEntry,
	isWithin,
	joinPath,
	parentPath,
} from "../../lib/file-source";
import { Icon } from "../ui/icons";
import { ManualCopy } from "../ui/ManualCopy";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
	tabClass,
} from "../ui/primitives";
import { dropDrafts, moveDrafts, useDraftPaths } from "./drafts";
import { type DirState, type FileAction, FileTree } from "./FileTree";
import { FileViewer } from "./FileViewer";
import {
	isOrbdOutdated,
	type RetryWait,
	useElementWidth,
	withWakeRetry,
} from "./hooks";
import {
	batchFromDrop,
	batchFromFiles,
	UploadQueue,
	useUploads,
} from "./UploadQueue";

/** 左树右内容的最小容器宽度。 */
export const SPLIT_MIN_WIDTH = 720;

interface Root {
	key: string;
	source: FileSource;
	label: string;
	path: string;
}

interface SourceRoots {
	source: FileSource;
	roots: Root[];
	error: unknown;
}

interface Persisted {
	root?: string;
	selected?: string | null;
}

function readPersisted(storageKey: string): Persisted {
	try {
		return JSON.parse(
			localStorage.getItem(`oryn.files.${storageKey}`) ?? "{}",
		) as Persisted;
	} catch {
		return {};
	}
}

type NameDialog = {
	kind: "newFile" | "newFolder" | "rename";
	entry: FileEntry | null;
	dir: string;
};

/** 名称输入校验：不得为空、不得含 `..` 段；仅新建文件允许 `/`（自动建父目录）。 */
function nameError(name: string, allowSlash: boolean): string | null {
	const n = name.trim();
	if (!n) return "files.dialog.nameRequired";
	if (!allowSlash && n.includes("/")) return "files.dialog.noSlash";
	if (n.split("/").some((seg) => seg === ".." || seg === "." || seg === ""))
		return "files.dialog.invalidName";
	return null;
}

/** 「发布为公开链接」：只对 `sourceId` 这个来源（orb）的文件开放；`run` 返回公开 URL。 */
export interface FilePublisher {
	sourceId: string;
	run(path: string): Promise<string>;
}

export function FileBrowser({
	sources,
	storageKey,
	refreshKey = 0,
	publisher,
	renderError,
	openPath,
	onOpened,
}: {
	sources: FileSource[];
	storageKey: string;
	/** 变化时刷新已展开的目录 */
	refreshKey?: number;
	publisher?: FilePublisher;
	/** 来源取根失败时的自定义提示（如停机预览不可用时附带唤醒按钮）；返回 null 则用缺省的错误文案 */
	renderError?(error: unknown, retry: () => void): ReactNode;
	/** 外部请求打开的文件（绝对路径）；`n` 变化即重新处理一次 */
	openPath?: { path: string; n: number };
	/** `openPath` 处理完毕（已打开，或不在任何根下而放弃） */
	onOpened?(): void;
}): ReactNode {
	const { t } = useTranslation();
	const hostRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(hostRef, SPLIT_MIN_WIDTH);
	const split = width >= SPLIT_MIN_WIDTH;

	const [sourceRoots, setSourceRoots] = useState<SourceRoots[] | null>(null);
	const [wait, setWait] = useState<RetryWait | null>(null);
	const [persisted, setPersisted] = useState<Persisted>(() =>
		readPersisted(storageKey),
	);
	const [dirs, setDirs] = useState<Record<string, DirState>>({});
	const [expanded, setExpanded] = useState<Record<string, boolean>>({});
	const [dialog, setDialog] = useState<NameDialog | null>(null);
	const [name, setName] = useState("");
	const [confirmDelete, setConfirmDelete] = useState<FileEntry | null>(null);
	const [opError, setOpError] = useState<unknown>(null);
	/** 操作成功后的一行提示（发布后「已复制公开链接」） */
	// 最近一次发布：公开链接，以及自动复制是否真的成功（失败时留下链接手动复制）
	const [published, setPublished] = useState<{
		url: string;
		copied: boolean;
	} | null>(null);
	const [busy, setBusy] = useState(false);
	const [rootsKey, setRootsKey] = useState(0);
	const fileInput = useRef<HTMLInputElement>(null);
	const nameId = useId();
	const uploadDir = useRef<string>("");

	// biome-ignore lint/correctness/useExhaustiveDependencies: rootsKey 触发重试
	useEffect(() => {
		let cancelled = false;
		setSourceRoots(null);
		void Promise.all(
			sources.map(async (source): Promise<SourceRoots> => {
				try {
					const roots = await withWakeRetry(
						() => source.roots(),
						(w) => {
							if (!cancelled) setWait(w);
						},
					);
					return {
						source,
						roots: roots.map((r) => ({
							...r,
							source,
							key: `${source.id}|${r.path}`,
						})),
						error: null,
					};
				} catch (error) {
					return { source, roots: [], error };
				}
			}),
		).then((r) => {
			if (!cancelled) setSourceRoots(r);
		});
		return () => {
			cancelled = true;
		};
	}, [sources, rootsKey]);

	const allRoots = useMemo(
		() => sourceRoots?.flatMap((s) => s.roots) ?? [],
		[sourceRoots],
	);
	const root: Root | undefined =
		allRoots.find((r) => r.key === persisted.root) ?? allRoots[0];
	const selected =
		root && persisted.root === root.key ? (persisted.selected ?? null) : null;
	const failed = sourceRoots?.filter((s) => s.error) ?? [];

	const persist = useCallback(
		(p: Persisted) => {
			setPersisted(p);
			try {
				localStorage.setItem(`oryn.files.${storageKey}`, JSON.stringify(p));
			} catch {
				// 隐私模式等不可写时只保留在内存
			}
		},
		[storageKey],
	);

	const source = root?.source;
	const rootPath = root?.path;
	const draftPaths = useDraftPaths(source?.id);

	// 「来源 + 根」的代次：换根时递增，之前发出的目录请求结果作废
	const generation = useRef(0);
	// 每个目录最近一次请求的序号：同目录的并发请求只认最后一个
	const dirRequests = useRef(new Map<string, number>());
	const requestSeq = useRef(0);

	const loadDir = useCallback(
		async (path: string) => {
			if (!source) return;
			const gen = generation.current;
			const n = ++requestSeq.current;
			dirRequests.current.set(path, n);
			const current = () =>
				gen === generation.current && dirRequests.current.get(path) === n;
			setDirs((d) =>
				d[path]?.status === "ok" ? d : { ...d, [path]: { status: "loading" } },
			);
			try {
				const entries = await withWakeRetry(
					() => source.list(path),
					(w) => {
						if (gen === generation.current) setWait(w);
					},
				);
				if (current())
					setDirs((d) => ({ ...d, [path]: { status: "ok", entries } }));
			} catch (error) {
				if (current())
					setDirs((d) => ({ ...d, [path]: { status: "error", error } }));
			}
		},
		[source],
	);

	// 换根：作废旧请求、清空树状态并加载根目录
	useEffect(() => {
		generation.current++;
		dirRequests.current.clear();
		setDirs({});
		setExpanded({});
		if (rootPath !== undefined) void loadDir(rootPath);
	}, [rootPath, loadDir]);

	// 外部请求打开文件：先切到包含它的根（取最深的一个），再逐级展开父目录并选中
	const openedN = useRef<number | null>(null);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只在请求、根列表与当前根变化时处理
	useEffect(() => {
		if (!openPath || allRoots.length === 0 || openedN.current === openPath.n)
			return;
		const { path, n } = openPath;
		let owner: Root | undefined;
		// 路径为空的根（Agent 配置）是相对路径空间，不收绝对路径
		for (const r of allRoots)
			if (
				r.path !== "" &&
				isWithin(path, r.path) &&
				(!owner || r.path.length > owner.path.length)
			)
				owner = r;
		if (!owner) {
			openedN.current = n;
			onOpened?.();
			return;
		}
		if (owner.key !== root?.key) {
			// 换根后 rootPath 变化，本 effect 重跑时走下面的分支
			persist({ root: owner.key, selected: path });
			return;
		}
		openedN.current = n;
		const top = owner.path;
		void (async () => {
			const chain: string[] = [];
			for (
				let p = parentPath(path);
				p !== "" && p !== top && isWithin(p, top);
				p = parentPath(p)
			)
				chain.unshift(p);
			for (const p of chain) {
				expandTo(p);
				await loadDir(p);
			}
			select(path);
			onOpened?.();
		})();
	}, [openPath?.n, allRoots, rootPath]);

	const expandedRef = useRef(expanded);
	expandedRef.current = expanded;
	const refreshAll = useCallback(() => {
		if (rootPath === undefined) return;
		void loadDir(rootPath);
		for (const [p, open] of Object.entries(expandedRef.current))
			if (open) void loadDir(p);
	}, [rootPath, loadDir]);

	const firstRefresh = useRef(true);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 只在 refreshKey 变化时刷新
	useEffect(() => {
		if (firstRefresh.current) {
			firstRefresh.current = false;
			return;
		}
		refreshAll();
	}, [refreshKey]);

	const uploads = useUploads((dir) => void loadDir(dir));

	const select = (path: string | null) => {
		if (!root) return;
		persist({ root: root.key, selected: path });
	};

	const download = (path: string) => {
		const url = source?.downloadUrl(path);
		if (!url) return;
		const a = document.createElement("a");
		a.href = url;
		a.download = baseName(path);
		document.body.appendChild(a);
		a.click();
		a.remove();
	};

	const run = async (fn: () => Promise<void>) => {
		setBusy(true);
		setOpError(null);
		setPublished(null);
		try {
			await fn();
			return true;
		} catch (e) {
			setOpError(e);
			return false;
		} finally {
			setBusy(false);
		}
	};

	const onAction = (action: FileAction, entry: FileEntry | null) => {
		if (!source || rootPath === undefined) return;
		const dir = entry
			? isDirEntry(entry)
				? entry.path
				: parentPath(entry.path)
			: rootPath;
		switch (action) {
			case "newFile":
			case "newFolder":
				setName("");
				setDialog({ kind: action, entry: null, dir });
				return;
			case "rename":
				if (!entry) return;
				setName(entry.name);
				setDialog({ kind: "rename", entry, dir: parentPath(entry.path) });
				return;
			case "delete":
				if (entry) setConfirmDelete(entry);
				return;
			case "download":
				if (entry) download(entry.path);
				return;
			case "copyPath":
				if (entry) void navigator.clipboard?.writeText(entry.path);
				return;
			case "publish":
				if (!entry || !publisher) return;
				void run(async () => {
					const url = await publisher.run(entry.path);
					setPublished({ url, copied: await copyToClipboard(url) });
				});
				return;
			case "upload":
				uploadDir.current = dir;
				fileInput.current?.click();
				return;
		}
	};

	const expandTo = (dir: string) => {
		if (rootPath === undefined || dir === rootPath) return;
		setExpanded((e) => ({ ...e, [dir]: true }));
	};

	const submitName = async () => {
		if (!dialog || !source) return;
		const n = name.trim();
		const ok = await run(async () => {
			if (dialog.kind === "rename" && dialog.entry) {
				const to = joinPath(dialog.dir, n);
				if (to === dialog.entry.path) return;
				await source.rename(dialog.entry.path, to);
				moveDrafts(source.id, dialog.entry.path, to);
				if (selected && isWithin(selected, dialog.entry.path))
					select(to + selected.slice(dialog.entry.path.length));
				await loadDir(dialog.dir);
				return;
			}
			const target = joinPath(dialog.dir, n);
			if (dialog.kind === "newFolder") {
				await source.mkdir(target);
			} else {
				if (n.includes("/") && source.caps.mkdir)
					await source.mkdir(parentPath(target));
				await source.write(target, "", { overwrite: false });
				select(target);
			}
			await loadDir(dialog.dir);
			expandTo(dialog.dir);
			// 新建文件的路径带子目录时，逐级展开并加载到其所在目录
			const chain: string[] = [];
			for (
				let p = parentPath(target);
				p !== dialog.dir && isWithin(p, dialog.dir);
				p = parentPath(p)
			)
				chain.unshift(p);
			for (const p of chain) {
				expandTo(p);
				await loadDir(p);
			}
		});
		if (ok) setDialog(null);
	};

	const doDelete = async (entry: FileEntry) => {
		const ok = await run(async () => {
			if (!source) return;
			await source.remove(entry.path);
			dropDrafts(source.id, entry.path);
			if (selected && isWithin(selected, entry.path)) select(null);
			await loadDir(parentPath(entry.path) || (rootPath ?? ""));
		});
		if (ok) setConfirmDelete(null);
	};

	const dialogErr = dialog ? nameError(name, dialog.kind === "newFile") : null;

	const header = (
		<div className="flex h-10 shrink-0 items-center gap-1 border-border border-b px-2">
			<div
				role="tablist"
				className="focus-inset flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto"
			>
				{allRoots.map((r) => {
					const on = r.key === root?.key;
					return (
						<button
							key={r.key}
							type="button"
							role="tab"
							aria-selected={on}
							title={r.path || undefined}
							className={`${tabClass(on)} ${on ? "bg-hover" : ""}`}
							onClick={() => persist({ root: r.key, selected: null })}
						>
							{t(r.label as "files.roots.home")}
						</button>
					);
				})}
			</div>
			{source && rootPath !== undefined && (
				<div className="flex shrink-0 items-center">
					{source.caps.write && (
						<IconButton
							icon="filePlus"
							size={15}
							label={t("files.actions.newFile")}
							onClick={() => onAction("newFile", null)}
						/>
					)}
					{source.caps.write && source.caps.mkdir && (
						<IconButton
							icon="folderPlus"
							size={15}
							label={t("files.actions.newFolder")}
							onClick={() => onAction("newFolder", null)}
						/>
					)}
					{source.caps.write && source.caps.upload && (
						<IconButton
							icon="upload"
							size={15}
							label={t("files.actions.upload")}
							onClick={() => onAction("upload", null)}
						/>
					)}
					<IconButton
						icon="refresh"
						size={15}
						label={t("files.actions.refresh")}
						onClick={refreshAll}
					/>
				</div>
			)}
		</div>
	);

	const tree =
		source && rootPath !== undefined ? (
			<FileTree
				rootPath={rootPath}
				dirs={dirs}
				expanded={expanded}
				selectedPath={selected}
				dirtyPaths={draftPaths}
				caps={{ ...source.caps, publish: publisher?.sourceId === source.id }}
				onToggle={(e) => {
					const open = !expanded[e.path];
					setExpanded((x) => ({ ...x, [e.path]: open }));
					if (open) void loadDir(e.path);
				}}
				onOpen={(e) => select(e.path)}
				onAction={onAction}
				onDropFiles={(dir, dt) => {
					void batchFromDrop(dt, dir).then(
						(batch) =>
							run(async () => {
								if (batch.dropped > 0)
									throw new Error(
										t("files.upload.tooMany", { n: batch.dropped }),
									);
								if (source) await uploads.enqueue(batch, source);
							}),
						setOpError,
					);
				}}
			/>
		) : null;

	const viewer =
		source && selected ? (
			<FileViewer
				key={`${source.id}|${selected}`}
				source={source}
				path={selected}
				onBack={split ? undefined : () => select(null)}
				onDownload={download}
				readOnly={!source.caps.write}
				onSaved={() => void loadDir(parentPath(selected) || (rootPath ?? ""))}
			/>
		) : (
			<div className="flex h-full items-center justify-center">
				<EmptyState icon="fileText" title={t("files.pick")} />
			</div>
		);

	const waitText =
		wait === "indexing" ? t("files.offline.indexing") : t("files.waking");
	const retryRoots = () => setRootsKey((k) => k + 1);

	let body: ReactNode;
	if (!sourceRoots) {
		body = (
			<div className="flex items-center gap-2 p-3 text-muted text-sm">
				<Spinner />
				{wait && waitText}
			</div>
		);
	} else if (!root) {
		const e = failed[0]?.error;
		body = isOrbdOutdated(e) ? (
			<EmptyState icon="alert" title={t("files.outdated")} />
		) : (
			(renderError?.(e, retryRoots) ?? (
				<div className="p-3">
					<ErrorText error={e} />
					<Button size="sm" className="mt-2" onClick={retryRoots}>
						{t("files.retry")}
					</Button>
				</div>
			))
		);
	} else if (split) {
		body = (
			<div className="flex min-h-0 flex-1">
				<div className="w-72 shrink-0 overflow-y-auto border-border border-r">
					{tree}
				</div>
				<div className="min-w-0 flex-1">{viewer}</div>
			</div>
		);
	} else {
		body = selected ? (
			<div className="min-h-0 flex-1">{viewer}</div>
		) : (
			<div className="min-h-0 flex-1 overflow-y-auto">{tree}</div>
		);
	}

	return (
		<div ref={hostRef} className="flex h-full min-h-0 flex-col">
			{header}
			{wait && sourceRoots && (
				<p className="flex shrink-0 items-center gap-1.5 bg-warn/8 px-3 py-1.5 text-warn text-xs">
					<Spinner size={12} />
					{waitText}
				</p>
			)}
			{failed.length > 0 && root && (
				<div className="shrink-0 px-3 py-1.5 text-xs">
					{failed.map((f) =>
						isOrbdOutdated(f.error) ? (
							<p
								key={f.source.id}
								className="flex items-center gap-1.5 text-warn"
							>
								<Icon name="alert" size={13} className="shrink-0" />
								{t("files.outdated")}
							</p>
						) : (
							<Fragment key={f.source.id}>
								{renderError?.(f.error, retryRoots) ?? (
									<ErrorText error={f.error} />
								)}
							</Fragment>
						),
					)}
				</div>
			)}
			{opError ? (
				<div className="shrink-0 px-3 py-1">
					<ErrorText error={opError} />
				</div>
			) : null}
			{published && (
				<div className="flex shrink-0 flex-col gap-1.5 bg-ok/10 py-1 pr-1 pl-3">
					<div
						role="status"
						className="flex items-center gap-2 text-ok-ink text-xs"
					>
						<Icon name="checkCircle" size={13} className="shrink-0 text-ok" />
						<span className="min-w-0 flex-1">
							{published.copied
								? t("publish.orbFileDone")
								: t("publish.orbFilePublished")}
						</span>
						<Button
							size="sm"
							variant="ghost"
							onClick={() => setPublished(null)}
						>
							{t("common.dismiss")}
						</Button>
					</div>
					{!published.copied && (
						<div className="pr-2 pb-1.5">
							<ManualCopy
								text={published.url}
								label={t("publish.urlLabel")}
								message={t("publish.copyFailed")}
							/>
						</div>
					)}
				</div>
			)}
			{body}
			<UploadQueue uploads={uploads} />
			<input
				ref={fileInput}
				type="file"
				multiple
				hidden
				data-testid="file-upload-input"
				onChange={(e) => {
					const files = e.target.files ? Array.from(e.target.files) : [];
					e.target.value = "";
					if (files.length === 0) return;
					if (!source) return;
					void run(() =>
						uploads.enqueue(
							batchFromFiles(files, uploadDir.current || (rootPath ?? "")),
							source,
						),
					);
				}}
			/>
			<Dialog
				open={dialog !== null}
				onClose={() => setDialog(null)}
				title={dialog ? t(`files.dialog.${dialog.kind}Title`) : ""}
				footer={
					<>
						<Button onClick={() => setDialog(null)}>
							{t("common.cancel")}
						</Button>
						<Button
							variant="primary"
							disabled={busy || dialogErr !== null}
							onClick={() => void submitName()}
						>
							{dialog?.kind === "rename"
								? t("files.actions.rename")
								: t("common.create")}
						</Button>
					</>
				}
			>
				<form
					onSubmit={(e) => {
						e.preventDefault();
						if (!dialogErr) void submitName();
					}}
				>
					<label htmlFor={nameId} className="flex flex-col gap-1 text-sm">
						<span className="text-muted text-xs">
							{t("files.dialog.nameIn", { dir: dialog?.dir || "/" })}
						</span>
						<TextInput
							id={nameId}
							autoFocus
							aria-label={t("files.dialog.name")}
							className="font-mono"
							value={name}
							onChange={(e) => setName(e.target.value)}
						/>
					</label>
					{name.trim() && dialogErr && (
						<p className="mt-1 text-err text-xs">
							{t(dialogErr as "files.dialog.invalidName")}
						</p>
					)}
					<ErrorText error={opError} />
				</form>
			</Dialog>
			<Dialog
				open={confirmDelete !== null}
				onClose={() => setConfirmDelete(null)}
				title={t("files.dialog.deleteTitle")}
				footer={
					<>
						<Button onClick={() => setConfirmDelete(null)}>
							{t("common.cancel")}
						</Button>
						<Button
							variant="danger"
							disabled={busy}
							onClick={() => confirmDelete && void doDelete(confirmDelete)}
						>
							{t("common.delete")}
						</Button>
					</>
				}
			>
				<p className="text-sm">
					{confirmDelete && isDirEntry(confirmDelete)
						? t("files.dialog.deleteDirBody", { path: confirmDelete.path })
						: t("files.dialog.deleteBody", { path: confirmDelete?.path ?? "" })}
				</p>
				<ErrorText error={opError} />
			</Dialog>
		</div>
	);
}
