/**
 * 文件树：目录懒加载（展开时由上层 `list`），右键 / 长按 / 行尾「⋯」/ Shift+F10 打开操作菜单，
 * 支持把文件拖到目录行（放进该目录）或空白处（放进当前根）。
 * 键盘按 WAI-ARIA tree 模式：整棵树一个 Tab 停靠点（roving tabindex），
 * ↑↓ 移动、→ 展开 / 进入首个子项、← 收起 / 回到父目录、Home / End 首尾、Enter / Space 打开或展开。
 */
import {
	type DragEvent,
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { type FileEntry, isDirEntry, isWithin } from "../../lib/file-source";
import { formatBytes } from "../../lib/images";
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
	| "copyPath";

export interface TreeCaps {
	mkdir: boolean;
	rename: boolean;
	upload: boolean;
	download: boolean;
}

/** 某个目标（`entry` 为空 = 当前根）可用的操作。 */
export function actionsFor(
	entry: FileEntry | null,
	caps: TreeCaps,
): FileAction[] {
	const dir = entry === null || isDirEntry(entry);
	const out: FileAction[] = [];
	if (dir) {
		out.push("newFile");
		if (caps.mkdir) out.push("newFolder");
		if (caps.upload) out.push("upload");
	}
	if (entry) {
		if (caps.rename) out.push("rename");
		if (!dir && caps.download) out.push("download");
		out.push("copyPath", "delete");
	}
	return out;
}

const ACTION_ICON: Record<FileAction, IconName> = {
	newFile: "file",
	newFolder: "folder",
	upload: "plus",
	rename: "edit",
	delete: "trash",
	download: "back",
	copyPath: "copy",
};

/** 目录列表上限（与 orbd 一致）；恰好达到时提示可能被截断。 */
const LIST_LIMIT = 5000;
const LONG_PRESS_MS = 500;

interface MenuState {
	entry: FileEntry | null;
	x: number;
	y: number;
}

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
	const [menu, setMenu] = useState<MenuState | null>(null);
	const [dropTarget, setDropTarget] = useState<string | null>(null);
	const press = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
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

	const openMenu = (entry: FileEntry | null, x: number, y: number) =>
		setMenu({ entry, x, y });

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
			case "ContextMenu":
			case "F10": {
				if (ev.key === "F10" && !ev.shiftKey) {
					handled = false;
					break;
				}
				const r = ev.currentTarget.getBoundingClientRect();
				openMenu(e, r.left + 24, r.bottom);
				break;
			}
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
		caps.upload
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
					className={`group flex h-7 cursor-pointer select-none items-center gap-1 rounded pr-1 text-sm outline-none hover:bg-hover focus-visible:ring-1 focus-visible:ring-accent ${selected ? "bg-hover text-fg" : "text-fg"} ${dropTarget === e.path ? "ring-1 ring-accent" : ""}`}
					style={{ paddingLeft: 4 + depth * 14 }}
					onClick={() => {
						setFocusPath(e.path);
						activate(e);
					}}
					onFocus={() => setFocusPath(e.path)}
					onKeyDown={(ev) => onRowKeyDown(ev, e)}
					onContextMenu={(ev) => {
						ev.preventDefault();
						openMenu(e, ev.clientX, ev.clientY);
					}}
					onPointerDown={(ev) => {
						if (ev.pointerType !== "touch") return;
						const { clientX, clientY } = ev;
						press.current = setTimeout(
							() => openMenu(e, clientX, clientY),
							LONG_PRESS_MS,
						);
					}}
					onPointerUp={() => clearTimeout(press.current)}
					onPointerMove={() => clearTimeout(press.current)}
					{...(dir ? dropProps(e.path) : {})}
				>
					<span className="flex w-3.5 shrink-0 justify-center text-faint">
						{dir && (
							<Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
						)}
					</span>
					<Icon
						name={dir ? "folder" : "file"}
						size={14}
						className="shrink-0 text-muted"
					/>
					<span className="min-w-0 flex-1 truncate">
						{e.name}
						{e.type === "symlink" && (
							<span className="text-faint text-xs">{` → ${e.target ?? ""}`}</span>
						)}
					</span>
					{dirty(e) && (
						<span
							role="img"
							aria-label={t("files.unsaved")}
							title={t("files.unsaved")}
							className="shrink-0 text-accent text-xs"
						>
							●
						</span>
					)}
					{!dir && (
						<span className="shrink-0 text-[10px] text-faint">
							{formatBytes(e.size)}
						</span>
					)}
					<button
						type="button"
						tabIndex={tabIndex}
						aria-label={t("files.actions.more", { name: e.name })}
						className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-faint opacity-60 hover:bg-raised hover:text-fg focus-visible:opacity-100 group-hover:opacity-100"
						onClick={(ev) => {
							ev.stopPropagation();
							const r = ev.currentTarget.getBoundingClientRect();
							openMenu(e, r.right, r.bottom);
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
			role="tree"
			aria-label={rootPath || "/"}
			className={`min-h-full p-1 pb-8 ${dropTarget === rootPath ? "bg-accent/5 ring-1 ring-accent ring-inset" : ""}`}
			onContextMenu={(ev) => {
				if (ev.target !== ev.currentTarget) return;
				ev.preventDefault();
				openMenu(null, ev.clientX, ev.clientY);
			}}
			{...dropProps(rootPath)}
		>
			{renderDir(rootPath, 0)}
			{menu && (
				<ContextMenu
					menu={menu}
					actions={actionsFor(menu.entry, caps)}
					onClose={(restoreFocus) => {
						setMenu(null);
						if (restoreFocus && menu.entry) focusRow(menu.entry.path);
					}}
					onAction={(a) => {
						setMenu(null);
						// 先把焦点还给行，后续对话框关闭时回到这里
						if (menu.entry) focusRow(menu.entry.path);
						onAction(a, menu.entry);
					}}
				/>
			)}
		</div>
	);
}

function ContextMenu({
	menu,
	actions,
	onClose,
	onAction,
}: {
	menu: MenuState;
	actions: FileAction[];
	/** `restoreFocus`：由键盘（Escape）关闭，焦点回到打开菜单的行 */
	onClose(restoreFocus: boolean): void;
	onAction(a: FileAction): void;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLDivElement>(null);
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;
	// 打开即聚焦第一项，键盘用户可直接 ↑↓ / Enter
	useEffect(() => {
		ref.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
	}, []);
	useEffect(() => {
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node))
				onCloseRef.current(false);
		};
		const onKey = (e: globalThis.KeyboardEvent) => {
			if (e.key === "Escape") onCloseRef.current(true);
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, []);
	const onMenuKeyDown = (ev: KeyboardEvent<HTMLDivElement>) => {
		const items = [
			...(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ??
				[]),
		];
		const i = items.indexOf(document.activeElement as HTMLElement);
		let next: HTMLElement | undefined;
		if (ev.key === "ArrowDown") next = items[(i + 1) % items.length];
		else if (ev.key === "ArrowUp")
			next = items[(i - 1 + items.length) % items.length];
		else if (ev.key === "Home") next = items[0];
		else if (ev.key === "End") next = items.at(-1);
		else if (ev.key === "Tab") {
			ev.preventDefault();
			onClose(true);
			return;
		} else return;
		ev.preventDefault();
		next?.focus();
	};
	// 贴近视口边缘时向内收，避免菜单出屏
	const left = Math.max(4, Math.min(menu.x, window.innerWidth - 220));
	const top = Math.max(
		4,
		Math.min(menu.y, window.innerHeight - 40 * actions.length),
	);
	return (
		<div
			ref={ref}
			role="menu"
			tabIndex={-1}
			onKeyDown={onMenuKeyDown}
			className="fixed z-50 min-w-48 rounded-lg border border-border bg-panel p-1 shadow-lg"
			style={{ left, top }}
		>
			{actions.map((a) => (
				<button
					key={a}
					type="button"
					role="menuitem"
					className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-hover ${a === "delete" ? "text-err" : "text-fg"}`}
					onClick={() => onAction(a)}
				>
					<Icon
						name={ACTION_ICON[a]}
						size={14}
						className={`shrink-0 text-muted ${a === "download" ? "-rotate-90" : ""}`}
					/>
					{t(`files.actions.${a}`)}
				</button>
			))}
		</div>
	);
}
