/**
 * 文件树：目录懒加载（展开时由上层 `list`）；操作菜单由统一上下文菜单（`lib/context-menu`）提供，
 * 本文件只登记菜单项（右键 / 长按 / Shift+F10 由全局分发器处理）与行尾「⋯」。
 * 支持把文件拖到目录行（放进该目录）或空白处（放进当前根）。
 * 键盘按 WAI-ARIA tree 模式：整棵树一个 Tab 停靠点（roving tabindex），
 * ↑↓ 移动、→ 展开 / 进入首个子项、← 收起 / 回到父目录、Home / End 首尾、Enter / Space 打开或展开。
 */
import {
	type DragEvent,
	type KeyboardEvent,
	type ReactNode,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useContextTarget } from "../../lib/context-menu/registry";
import { openContextMenu } from "../../lib/context-menu/store";
import type { MenuGroup } from "../../lib/context-menu/types";
import { type FileEntry, isDirEntry, isWithin } from "../../lib/file-source";
import { formatBytes } from "../../lib/format";
import { FileTypeIcon } from "../ui/FileTypeIcon";
import { Icon, type IconName } from "../ui/icons";
import { describeError, Spinner } from "../ui/primitives";

export type DirState =
	| { status: "loading" }
	| { status: "error"; error: unknown }
	| { status: "ok"; entries: FileEntry[] };

export type FileAction =
	| "newFile"
	| "newFolder"
	| "upload"
	| "rename"
	| "delete"
	| "download"
	| "copyPath"
	| "publish";

export interface TreeCaps {
	/** 可写；为 false 时只剩下载、复制路径与发布，也不接受拖放上传 */
	write: boolean;
	mkdir: boolean;
	rename: boolean;
	upload: boolean;
	download: boolean;
	/** 文件可发布为公开链接（仅 orb 来源） */
	publish?: boolean;
}

/** 某个目标（`entry` 为空 = 当前根）可用的操作。 */
export function actionsFor(
	entry: FileEntry | null,
	caps: TreeCaps,
): FileAction[] {
	const dir = entry === null || isDirEntry(entry);
	const out: FileAction[] = [];
	if (dir && caps.write) {
		out.push("newFile");
		if (caps.mkdir) out.push("newFolder");
		if (caps.upload) out.push("upload");
	}
	if (entry) {
		if (caps.write && caps.rename) out.push("rename");
		if (!dir && caps.download) out.push("download");
		if (!dir && caps.publish) out.push("publish");
		out.push("copyPath");
		if (caps.write) out.push("delete");
	}
	return out;
}

const ACTION_ICON: Record<FileAction, IconName> = {
	newFile: "filePlus",
	newFolder: "folderPlus",
	upload: "upload",
	rename: "edit",
	delete: "trash",
	download: "download",
	copyPath: "copy",
	publish: "globe",
};

/** 目录列表上限（与 orbd 一致）；恰好达到时提示可能被截断。 */
const LIST_LIMIT = 5000;

interface VisibleItem {
	entry: FileEntry;
	depth: number;
	/** 所在目录 */
	parent: string;
	posinset: number;
	setsize: number;
}

/** 当前可见（祖先均已展开且已加载）的树项，按显示顺序。 */
export function visibleItems(
	rootPath: string,
	dirs: Record<string, DirState>,
	expanded: Record<string, boolean>,
): VisibleItem[] {
	const out: VisibleItem[] = [];
	const walk = (dir: string, depth: number) => {
		const st = dirs[dir];
		if (st?.status !== "ok") return;
		st.entries.forEach((entry, i) => {
			out.push({
				entry,
				depth,
				parent: dir,
				posinset: i + 1,
				setsize: st.entries.length,
			});
			if (isDirEntry(entry) && expanded[entry.path])
				walk(entry.path, depth + 1);
		});
	};
	walk(rootPath, 0);
	return out;
}

export function FileTree({
	rootPath,
	dirs,
	expanded,
	selectedPath,
	dirtyPaths = [],
	caps,
	onToggle,
	onOpen,
	onAction,
	onDropFiles,
}: {
	rootPath: string;
	dirs: Record<string, DirState>;
	expanded: Record<string, boolean>;
	selectedPath: string | null;
	/** 有未保存草稿的文件；其所在目录也会标出 */
	dirtyPaths?: readonly string[];
	caps: TreeCaps;
	onToggle(entry: FileEntry): void;
	onOpen(entry: FileEntry): void;
	onAction(action: FileAction, entry: FileEntry | null): void;
	onDropFiles(dir: string, dt: DataTransfer): void;
}): ReactNode {
	const { t } = useTranslation();
	const [dropTarget, setDropTarget] = useState<string | null>(null);
	const [focusPath, setFocusPath] = useState<string | null>(null);
	const rows = useRef(new Map<string, HTMLDivElement>());

	const visible = useMemo(
		() => visibleItems(rootPath, dirs, expanded),
		[rootPath, dirs, expanded],
	);
	const index = useMemo(
		() => new Map(visible.map((v, i) => [v.entry.path, i])),
		[visible],
	);
	// roving tabindex：唯一可 Tab 进入的项 = 最近聚焦的项 → 已选中的项 → 第一项
	const tabbable =
		(focusPath !== null && index.has(focusPath) && focusPath) ||
		(selectedPath !== null && index.has(selectedPath) && selectedPath) ||
		visible[0]?.entry.path;

	const focusRow = (path: string | undefined) => {
		if (path === undefined) return;
		setFocusPath(path);
		rows.current.get(path)?.focus();
	};

	// 没有可用操作（只读来源的根）时不弹出空菜单
	const groupsFor = (entry: FileEntry | null): MenuGroup[] | null => {
		const actions = actionsFor(entry, caps);
		if (actions.length === 0) return null;
		return [
			{
				key: "file",
				items: actions.map((a) => ({
					key: a,
					icon: ACTION_ICON[a],
					label:
						a === "publish" ? t("publish.orbFile") : t(`files.actions.${a}`),
					danger: a === "delete",
					onSelect: () => {
						// 先把焦点还给行，后续对话框关闭时回到这里
						if (entry) focusRow(entry.path);
						onAction(a, entry);
					},
				})),
			},
		];
	};

	const treeRef = useContextTarget({
		items: (req) => {
			const row = req.target.closest('[role="treeitem"][data-path]');
			if (row) {
				const i = index.get(row.getAttribute("data-path") ?? "");
				const entry = i === undefined ? undefined : visible[i]?.entry;
				return entry ? groupsFor(entry) : null;
			}
			// 树里的空白处 = 当前根
			return req.target.getAttribute("role") === "tree"
				? groupsFor(null)
				: null;
		},
	});

	const activate = (e: FileEntry) => (isDirEntry(e) ? onToggle(e) : onOpen(e));

	const onRowKeyDown = (ev: KeyboardEvent<HTMLDivElement>, e: FileEntry) => {
		// 行内按钮自己的按键不当作对行的操作
		if (ev.target !== ev.currentTarget) return;
		const i = index.get(e.path);
		if (i === undefined) return;
		const item = visible[i];
		const dir = isDirEntry(e);
		const open = dir && Boolean(expanded[e.path]);
		let handled = true;
		switch (ev.key) {
			case "ArrowDown":
				focusRow(visible[i + 1]?.entry.path);
				break;
			case "ArrowUp":
				focusRow(visible[i - 1]?.entry.path);
				break;
			case "Home":
				focusRow(visible[0]?.entry.path);
				break;
			case "End":
				focusRow(visible.at(-1)?.entry.path);
				break;
			case "ArrowRight":
				if (dir && !open) onToggle(e);
				else if (open && visible[i + 1]?.parent === e.path)
					focusRow(visible[i + 1]?.entry.path);
				break;
			case "ArrowLeft":
				if (open) onToggle(e);
				else if (item && item.parent !== rootPath) focusRow(item.parent);
				break;
			case "Enter":
			case " ":
				activate(e);
				break;
			default:
				handled = false;
		}
		if (handled) {
			ev.preventDefault();
			ev.stopPropagation();
		}
	};

	const dirty = (e: FileEntry) =>
		isDirEntry(e)
			? dirtyPaths.some((p) => p !== e.path && isWithin(p, e.path))
			: dirtyPaths.includes(e.path);

	const dropProps = (dir: string) =>
		caps.write && caps.upload
			? {
					onDragOver: (e: DragEvent) => {
						if (!e.dataTransfer.types.includes("Files")) return;
						e.preventDefault();
						e.stopPropagation();
						setDropTarget(dir);
					},
					onDragLeave: (e: DragEvent) => {
						e.stopPropagation();
						setDropTarget((d) => (d === dir ? null : d));
					},
					onDrop: (e: DragEvent) => {
						e.preventDefault();
						e.stopPropagation();
						setDropTarget(null);
						onDropFiles(dir, e.dataTransfer);
					},
				}
			: {};

	const renderDir = (path: string, depth: number): ReactNode => {
		const st = dirs[path];
		if (!st || st.status === "loading")
			return (
				<div className="py-1" style={{ paddingLeft: 8 + depth * 14 }}>
					<Spinner size={12} />
				</div>
			);
		if (st.status === "error")
			return (
				<p
					role="alert"
					className="py-1 text-err text-xs"
					style={{ paddingLeft: 8 + depth * 14 }}
				>
					{describeError(st.error, t)}
				</p>
			);
		if (st.entries.length === 0)
			return (
				<p
					className="py-1 text-faint text-xs"
					style={{ paddingLeft: 8 + depth * 14 }}
				>
					{t("files.emptyDir")}
				</p>
			);
		return (
			<>
				{st.entries.map((e) => renderEntry(e, depth))}
				{st.entries.length >= LIST_LIMIT && (
					<p
						className="py-1 text-warn text-xs"
						style={{ paddingLeft: 8 + depth * 14 }}
					>
						{t("files.listTruncated", { n: LIST_LIMIT })}
					</p>
				)}
			</>
		);
	};

	const renderEntry = (e: FileEntry, depth: number): ReactNode => {
		const dir = isDirEntry(e);
		const open = dir && expanded[e.path];
		const selected = selectedPath === e.path;
		const item = visible[index.get(e.path) ?? -1];
		const tabIndex = tabbable === e.path ? 0 : -1;
		return (
			<div key={e.path} role="none">
				<div
					ref={(el) => {
						if (el) rows.current.set(e.path, el);
						else rows.current.delete(e.path);
					}}
					role="treeitem"
					// 名称只取文件名：行内按钮、大小等不进入树项的可访问名称
					aria-label={e.name}
					aria-expanded={dir ? Boolean(open) : undefined}
					aria-selected={selected}
					aria-level={depth + 1}
					aria-posinset={item?.posinset}
					aria-setsize={item?.setsize}
					tabIndex={tabIndex}
					data-path={e.path}
					className={`group flex h-7 cursor-pointer select-none items-center gap-1.5 rounded-sm pr-1 text-sm transition-colors duration-150 ${selected ? "bg-hover font-medium text-fg" : "text-fg hover:bg-hover/60"} ${dropTarget === e.path ? "ring-1 ring-accent ring-inset" : ""}`}
					style={{ paddingLeft: 4 + depth * 14 }}
					onClick={() => {
						setFocusPath(e.path);
						activate(e);
					}}
					onFocus={() => setFocusPath(e.path)}
					onKeyDown={(ev) => onRowKeyDown(ev, e)}
					data-ctx-touch=""
					{...(dir ? dropProps(e.path) : {})}
				>
					<span className="flex w-3 shrink-0 justify-center text-faint">
						{dir && (
							<Icon name={open ? "chevronDown" : "chevronRight"} size={11} />
						)}
					</span>
					<FileTypeIcon
						name={e.name}
						kind={dir ? "dir" : e.type === "symlink" ? "symlink" : "file"}
						open={open}
						size={15}
					/>
					<span className="min-w-0 flex-1 truncate">
						{e.name}
						{e.type === "symlink" && (
							<span className="font-normal text-faint text-xs">{` → ${e.target ?? ""}`}</span>
						)}
					</span>
					{dirty(e) && (
						<span
							role="img"
							aria-label={t("files.unsaved")}
							title={t("files.unsaved")}
							className="mx-1 h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
						/>
					)}
					{!dir && (
						<span className="shrink-0 font-normal text-2xs text-faint tabular-nums">
							{formatBytes(e.size)}
						</span>
					)}
					<button
						type="button"
						tabIndex={tabIndex}
						aria-label={t("files.actions.more", { name: e.name })}
						className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-faint opacity-60 transition-colors duration-150 hover:bg-active hover:text-fg focus-visible:opacity-100 group-hover:opacity-100"
						onClick={(ev) => {
							ev.stopPropagation();
							const r = ev.currentTarget.getBoundingClientRect();
							const groups = groupsFor(e);
							if (groups)
								openContextMenu({
									groups,
									x: r.right,
									y: r.bottom,
									trigger: "keyboard",
									restoreFocus: rows.current.get(e.path) ?? null,
								});
						}}
						// Enter / Space 只作用于按钮本身，不冒泡成对行的打开 / 展开
						onKeyDown={(ev) => ev.stopPropagation()}
					>
						<Icon name="more" size={14} />
					</button>
				</div>
				{open && (
					// biome-ignore lint/a11y/useSemanticElements: WAI-ARIA tree 的子项分组，不是表单字段集
					<div role="group">{renderDir(e.path, depth + 1)}</div>
				)}
			</div>
		);
	};

	return (
		<div
			ref={treeRef}
			role="tree"
			aria-label={rootPath || "/"}
			className={`focus-inset min-h-full p-1 pb-8 ${dropTarget === rootPath ? "bg-accent/5 ring-1 ring-accent ring-inset" : ""}`}
			{...dropProps(rootPath)}
		>
			{renderDir(rootPath, 0)}
		</div>
	);
}
