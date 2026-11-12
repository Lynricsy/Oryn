/**
 * 文件树：目录懒加载（展开时由上层 `list`），右键 / 长按 / 行尾「⋯」打开操作菜单，
 * 支持把文件拖到目录行（放进该目录）或空白处（放进当前根）。
 */
import {
	type DragEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { type FileEntry, isDirEntry } from "../../lib/file-source";
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

export function FileTree({
	rootPath,
	dirs,
	expanded,
	selectedPath,
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

	const openMenu = (entry: FileEntry | null, x: number, y: number) =>
		setMenu({ entry, x, y });

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
		return (
			<div key={e.path} role="none">
				<div
					role="treeitem"
					aria-expanded={dir ? Boolean(open) : undefined}
					aria-selected={selected}
					tabIndex={-1}
					data-path={e.path}
					className={`group flex h-7 cursor-pointer select-none items-center gap-1 rounded pr-1 text-sm hover:bg-hover ${selected ? "bg-hover text-fg" : "text-fg"} ${dropTarget === e.path ? "ring-1 ring-accent" : ""}`}
					style={{ paddingLeft: 4 + depth * 14 }}
					onClick={() => (dir ? onToggle(e) : onOpen(e))}
					onKeyDown={(ev) => {
						if (ev.key === "Enter") dir ? onToggle(e) : onOpen(e);
					}}
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
					{!dir && (
						<span className="shrink-0 text-[10px] text-faint">
							{formatBytes(e.size)}
						</span>
					)}
					<button
						type="button"
						aria-label={t("files.actions.more", { name: e.name })}
						className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-faint opacity-60 hover:bg-raised hover:text-fg group-hover:opacity-100"
						onClick={(ev) => {
							ev.stopPropagation();
							const r = ev.currentTarget.getBoundingClientRect();
							openMenu(e, r.right, r.bottom);
						}}
					>
						<Icon name="more" size={14} />
					</button>
				</div>
				{open && renderDir(e.path, depth + 1)}
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
					onClose={() => setMenu(null)}
					onAction={(a) => {
						setMenu(null);
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
	onClose(): void;
	onAction(a: FileAction): void;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node)) onClose();
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") onClose();
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [onClose]);
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
