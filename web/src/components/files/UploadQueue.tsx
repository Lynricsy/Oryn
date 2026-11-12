/**
 * 上传队列：并发 3，单文件超过 512MiB 在客户端直接拒绝；
 * 重名时先以 `overwrite=false` 写入，收到 409 `exists` 再询问「替换 / 跳过」。
 * 拖入文件夹时先按深度逐个 `mkdir`，再逐文件写入，一次最多 2000 个文件。
 */
import {
	type ReactNode,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "../../api/client";
import {
	type FileSource,
	joinPath,
	MAX_FILE_BYTES,
	parentPath,
} from "../../lib/file-source";
import { formatBytes } from "../../lib/images";
import { Button, Dialog, describeError, IconButton } from "../ui/primitives";

export const UPLOAD_CONCURRENCY = 3;
export const MAX_UPLOAD_FILES = 2000;

export type UploadStatus =
	| "queued"
	| "uploading"
	| "conflict"
	| "done"
	| "failed"
	| "skipped"
	| "rejected"
	| "cancelled";

export interface UploadItem {
	id: number;
	/** 目标绝对路径（agent 配置为相对路径） */
	path: string;
	file: File;
	/** 入队时的来源；换根后仍写往原来源 */
	source: FileSource;
	status: UploadStatus;
	sent: number;
	error?: string;
	overwrite: boolean;
}

/** 一批待上传的文件与需要预先创建的目录。 */
export interface UploadBatch {
	dirs: string[];
	files: { file: File; path: string }[];
	/** 超过 2000 个文件时被丢弃的数量 */
	dropped: number;
}

/** 普通文件列表（选择框 / 无目录信息的拖放）放进 `dir`。 */
export function batchFromFiles(
	files: Iterable<File>,
	dir: string,
): UploadBatch {
	const all = [...files];
	return {
		dirs: [],
		files: all
			.slice(0, MAX_UPLOAD_FILES)
			.map((file) => ({ file, path: joinPath(dir, file.name) })),
		dropped: Math.max(0, all.length - MAX_UPLOAD_FILES),
	};
}

interface FsEntryLike {
	isFile: boolean;
	isDirectory: boolean;
	name: string;
	file?(ok: (f: File) => void, err?: (e: unknown) => void): void;
	createReader?(): {
		readEntries(
			ok: (es: FsEntryLike[]) => void,
			err?: (e: unknown) => void,
		): void;
	};
}

function readAll(dir: FsEntryLike): Promise<FsEntryLike[]> {
	const reader = dir.createReader?.();
	if (!reader) return Promise.resolve([]);
	const out: FsEntryLike[] = [];
	return new Promise((resolve, reject) => {
		// readEntries 每次只返回一部分，读到空数组为止
		const next = () =>
			reader.readEntries((batch) => {
				if (batch.length === 0) resolve(out);
				else {
					out.push(...batch);
					next();
				}
			}, reject);
		next();
	});
}

function entryFile(e: FsEntryLike): Promise<File> {
	return new Promise((resolve, reject) => e.file?.(resolve, reject));
}

/**
 * 拖放数据 → 上传批次。目录经 `webkitGetAsEntry` 递归遍历；
 * 条目必须在 drop 事件同步阶段取出，之后 DataTransfer 即失效。
 */
export async function batchFromDrop(
	dt: DataTransfer,
	dir: string,
): Promise<UploadBatch> {
	const roots: FsEntryLike[] = [];
	for (const item of Array.from(dt.items ?? [])) {
		if (item.kind !== "file") continue;
		const entry = (
			item as DataTransferItem & { webkitGetAsEntry?(): FsEntryLike | null }
		).webkitGetAsEntry?.();
		if (entry) roots.push(entry);
	}
	if (roots.length === 0) return batchFromFiles(Array.from(dt.files), dir);
	const batch: UploadBatch = { dirs: [], files: [], dropped: 0 };
	const walk = async (e: FsEntryLike, into: string) => {
		const target = joinPath(into, e.name);
		if (e.isDirectory) {
			batch.dirs.push(target);
			for (const child of await readAll(e)) await walk(child, target);
		} else if (e.isFile) {
			if (batch.files.length >= MAX_UPLOAD_FILES) {
				batch.dropped++;
				return;
			}
			batch.files.push({ file: await entryFile(e), path: target });
		}
	};
	for (const r of roots) await walk(r, dir);
	return batch;
}

type ConflictChoice = "replace" | "skip";

export interface Uploads {
	items: UploadItem[];
	enqueue(batch: UploadBatch, source: FileSource): Promise<void>;
	cancel(id: number): void;
	clearFinished(): void;
	/** 当前等待用户决定的冲突 */
	conflict: UploadItem | null;
	resolveConflict(choice: ConflictChoice, applyToRest: boolean): void;
}

const FINISHED: Record<UploadStatus, boolean> = {
	queued: false,
	uploading: false,
	conflict: false,
	done: true,
	failed: true,
	skipped: true,
	rejected: true,
	cancelled: true,
};

/** 上传队列状态机。`onUploaded(dir)` 在每个文件写入成功后调用（刷新所在目录）。 */
export function useUploads(onUploaded: (dir: string) => void): Uploads {
	const { t } = useTranslation();
	const [items, setItems] = useState<UploadItem[]>([]);
	const itemsRef = useRef(items);
	itemsRef.current = items;
	const nextId = useRef(1);
	const aborts = useRef(new Map<number, AbortController>());
	const sticky = useRef<ConflictChoice | null>(null);
	const onUploadedRef = useRef(onUploaded);
	onUploadedRef.current = onUploaded;

	const patch = useCallback((id: number, p: Partial<UploadItem>) => {
		setItems((list) => list.map((it) => (it.id === id ? { ...it, ...p } : it)));
	}, []);

	const start = useCallback(
		async (it: UploadItem) => {
			const ctl = new AbortController();
			aborts.current.set(it.id, ctl);
			patch(it.id, { status: "uploading", sent: 0 });
			try {
				await it.source.write(it.path, it.file, {
					overwrite: it.overwrite,
					signal: ctl.signal,
					onProgress: (sent) => patch(it.id, { sent }),
				});
				patch(it.id, { status: "done", sent: it.file.size });
				onUploadedRef.current(parentPath(it.path));
			} catch (e) {
				if (e instanceof DOMException && e.name === "AbortError")
					patch(it.id, { status: "cancelled" });
				else if (e instanceof ApiError && e.code === "exists") {
					if (sticky.current === "skip") patch(it.id, { status: "skipped" });
					else if (sticky.current === "replace")
						patch(it.id, { status: "queued", overwrite: true });
					else patch(it.id, { status: "conflict" });
				} else patch(it.id, { status: "failed", error: describeError(e, t) });
			} finally {
				aborts.current.delete(it.id);
			}
		},
		[patch, t],
	);

	// 调度：保持至多 3 个上传中（已登记 AbortController 的视为已启动，防止副作用重复执行时重复上传）
	useEffect(() => {
		const running = items.filter((i) => i.status === "uploading").length;
		const free = UPLOAD_CONCURRENCY - running;
		if (free <= 0) return;
		for (const it of items
			.filter((i) => i.status === "queued" && !aborts.current.has(i.id))
			.slice(0, free))
			void start(it);
	}, [items, start]);

	const enqueue = useCallback(
		async (batch: UploadBatch, source: FileSource) => {
			if (source.caps.mkdir) {
				const dirs = [...new Set(batch.dirs)].sort(
					(a, b) => a.split("/").length - b.split("/").length,
				);
				for (const d of dirs) await source.mkdir(d);
			}
			sticky.current = null;
			const added = batch.files.map(
				({ file, path }): UploadItem => ({
					id: nextId.current++,
					path,
					file,
					source,
					sent: 0,
					overwrite: false,
					status: file.size > MAX_FILE_BYTES ? "rejected" : "queued",
					error:
						file.size > MAX_FILE_BYTES
							? t("files.upload.tooLarge", { max: formatBytes(MAX_FILE_BYTES) })
							: undefined,
				}),
			);
			setItems((list) => [...list, ...added]);
		},
		[t],
	);

	const conflict = items.find((i) => i.status === "conflict") ?? null;

	return {
		items,
		enqueue,
		cancel: (id) => {
			const ctl = aborts.current.get(id);
			if (ctl) ctl.abort();
			else patch(id, { status: "cancelled" });
		},
		clearFinished: () =>
			setItems((list) => list.filter((i) => !FINISHED[i.status])),
		conflict,
		resolveConflict: (choice, applyToRest) => {
			if (applyToRest) sticky.current = choice;
			setItems((list) =>
				list.map((it) => {
					if (it.status !== "conflict") return it;
					if (it.id !== conflict?.id && !applyToRest) return it;
					return choice === "replace"
						? { ...it, status: "queued", overwrite: true }
						: { ...it, status: "skipped" };
				}),
			);
		},
	};
}

/** 队列列表 + 冲突对话框。 */
export function UploadQueue({ uploads }: { uploads: Uploads }): ReactNode {
	const { t } = useTranslation();
	const [applyAll, setApplyAll] = useState(false);
	const { items, conflict } = uploads;
	if (items.length === 0) return null;
	const pending = items.filter((i) => !FINISHED[i.status]).length;
	return (
		<div className="border-border border-t bg-raised text-xs">
			<div className="flex items-center gap-2 px-2 py-1">
				<span className="min-w-0 flex-1 truncate font-medium text-muted">
					{t("files.upload.title", { pending, total: items.length })}
				</span>
				<Button size="sm" variant="ghost" onClick={uploads.clearFinished}>
					{t("files.upload.clear")}
				</Button>
			</div>
			<ul className="max-h-40 overflow-y-auto px-2 pb-1">
				{items.map((it) => (
					<li key={it.id} className="flex items-center gap-2 py-0.5">
						<span className="min-w-0 flex-1 truncate font-mono" title={it.path}>
							{it.file.name}
						</span>
						{it.status === "uploading" ? (
							<progress
								className="h-1.5 w-20"
								max={it.file.size || 1}
								value={it.sent}
								aria-label={it.file.name}
							/>
						) : (
							<span
								className={
									it.status === "failed" || it.status === "rejected"
										? "text-err"
										: "text-faint"
								}
								title={it.error}
							>
								{t(`files.upload.status.${it.status}`)}
							</span>
						)}
						{!FINISHED[it.status] && (
							<IconButton
								icon="close"
								size={12}
								className="h-5 w-5"
								label={t("common.cancel")}
								onClick={() => uploads.cancel(it.id)}
							/>
						)}
					</li>
				))}
			</ul>
			<Dialog
				open={conflict !== null}
				onClose={() => uploads.resolveConflict("skip", false)}
				title={t("files.upload.conflictTitle")}
				footer={
					<>
						<Button onClick={() => uploads.resolveConflict("skip", applyAll)}>
							{t("files.upload.skip")}
						</Button>
						<Button
							variant="primary"
							onClick={() => uploads.resolveConflict("replace", applyAll)}
						>
							{t("files.upload.replace")}
						</Button>
					</>
				}
			>
				<p className="text-sm">
					{t("files.upload.conflictBody", { path: conflict?.path ?? "" })}
				</p>
				<label className="mt-3 flex items-center gap-2 text-sm">
					<input
						type="checkbox"
						checked={applyAll}
						onChange={(e) => setApplyAll(e.target.checked)}
					/>
					{t("files.upload.applyAll")}
				</label>
			</Dialog>
		</div>
	);
}
