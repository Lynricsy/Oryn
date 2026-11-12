/**
 * 通用文件浏览器：根切换器 + 文件树 + 预览 / 编辑 + 上传队列。
 * 线程 Files 面板（orb + agent 配置）与设置页的 agent 配置共用。
 * 容器宽 ≥720px 时左树右内容，否则列表 → 详情两级。
 * 刷新时机：展开目录、手动刷新、自己的变更之后、`refreshKey` 变化（由上层在 agent 活动后防抖触发）。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	baseName,
	type FileEntry,
	type FileSource,
	isDirEntry,
	isWithin,
	joinPath,
	parentPath,
} from "../../lib/file-source";
import {
	Button,
	Dialog,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../ui/primitives";
import { type DirState, type FileAction, FileTree } from "./FileTree";
import { FileViewer } from "./FileViewer";
import { isOrbdOutdated, useElementWidth, withWakeRetry } from "./hooks";
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

export function FileBrowser({
	sources,
	storageKey,
	refreshKey = 0,
}: {
	sources: FileSource[];
	storageKey: string;
	/** 变化时刷新已展开的目录 */
	refreshKey?: number;
}): ReactNode {
	const { t } = useTranslation();
	const hostRef = useRef<HTMLDivElement>(null);
	const width = useElementWidth(hostRef, SPLIT_MIN_WIDTH);
	const split = width >= SPLIT_MIN_WIDTH;

	const [sourceRoots, setSourceRoots] = useState<SourceRoots[] | null>(null);
	const [waking, setWaking] = useState(false);
	const [persisted, setPersisted] = useState<Persisted>(() =>
		readPersisted(storageKey),
	);
	const [dirs, setDirs] = useState<Record<string, DirState>>({});
	const [expanded, setExpanded] = useState<Record<string, boolean>>({});
	const [dialog, setDialog] = useState<NameDialog | null>(null);
	const [name, setName] = useState("");
	const [confirmDelete, setConfirmDelete] = useState<FileEntry | null>(null);
	const [opError, setOpError] = useState<unknown>(null);
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
							if (!cancelled) setWaking(w);
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

	const loadDir = useCallback(
		async (path: string) => {
			if (!source) return;
			setDirs((d) =>
				d[path]?.status === "ok" ? d : { ...d, [path]: { status: "loading" } },
			);
			try {
				const entries = await withWakeRetry(() => source.list(path), setWaking);
				setDirs((d) => ({ ...d, [path]: { status: "ok", entries } }));
			} catch (error) {
				setDirs((d) => ({ ...d, [path]: { status: "error", error } }));
			}
		},
		[source],
	);

	// 换根：清空树状态并加载根目录
	useEffect(() => {
		setDirs({});
		setExpanded({});
		if (rootPath !== undefined) void loadDir(rootPath);
	}, [rootPath, loadDir]);

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
			await source?.remove(entry.path);
			if (selected && isWithin(selected, entry.path)) select(null);
			await loadDir(parentPath(entry.path) || (rootPath ?? ""));
		});
		if (ok) setConfirmDelete(null);
	};

	const dialogErr = dialog ? nameError(name, dialog.kind === "newFile") : null;

	const header = (
		<div className="flex shrink-0 flex-wrap items-center gap-1 border-border border-b px-2 py-1">
			<div role="tablist" className="flex min-w-0 flex-1 flex-wrap gap-1">
				{allRoots.map((r) => (
					<button
						key={r.key}
						type="button"
						role="tab"
						aria-selected={r.key === root?.key}
						title={r.path || undefined}
						className={`rounded-md px-2 py-1 text-xs ${r.key === root?.key ? "bg-hover text-fg" : "text-muted hover:text-fg"}`}
						onClick={() => persist({ root: r.key, selected: null })}
					>
						{t(r.label as "files.roots.home")}
					</button>
				))}
			</div>
			{source && rootPath !== undefined && (
				<>
					<IconButton
						icon="file"
						label={t("files.actions.newFile")}
						onClick={() => onAction("newFile", null)}
					/>
					{source.caps.mkdir && (
						<IconButton
							icon="folder"
							label={t("files.actions.newFolder")}
							onClick={() => onAction("newFolder", null)}
						/>
					)}
					{source.caps.upload && (
						<IconButton
							icon="plus"
							label={t("files.actions.upload")}
							onClick={() => onAction("upload", null)}
						/>
					)}
					<IconButton
						icon="clock"
						label={t("files.actions.refresh")}
						onClick={refreshAll}
					/>
				</>
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
				caps={source.caps}
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
				onSaved={() => void loadDir(parentPath(selected) || (rootPath ?? ""))}
			/>
		) : (
			<div className="flex h-full items-center justify-center p-6 text-faint text-sm">
				{t("files.pick")}
			</div>
		);

	let body: ReactNode;
	if (!sourceRoots) {
		body = (
			<div className="flex items-center gap-2 p-3 text-muted text-sm">
				<Spinner />
				{waking && t("files.waking")}
			</div>
		);
	} else if (!root) {
		const e = failed[0]?.error;
		body = isOrbdOutdated(e) ? (
			<EmptyState icon="alert" title={t("files.outdated")} />
		) : (
			<div className="p-3">
				<ErrorText error={e} />
				<Button
					size="sm"
					className="mt-2"
					onClick={() => setRootsKey((k) => k + 1)}
				>
					{t("files.retry")}
				</Button>
			</div>
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
			{waking && sourceRoots && (
				<p className="shrink-0 bg-warn/10 px-3 py-1 text-warn text-xs">
					{t("files.waking")}
				</p>
			)}
			{failed.length > 0 && root && (
				<div className="shrink-0 px-3 py-1 text-xs">
					{failed.map((f) =>
						isOrbdOutdated(f.error) ? (
							<p key={f.source.id} className="text-warn">
								{t("files.outdated")}
							</p>
						) : (
							<ErrorText key={f.source.id} error={f.error} />
						),
					)}
				</div>
			)}
			{opError ? (
				<div className="shrink-0 px-3 py-1">
					<ErrorText error={opError} />
				</div>
			) : null}
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
