/** 文件树键盘：WAI-ARIA tree（roving tabindex、方向键、Home / End、Enter / Space），行内「⋯」按钮的按键不冒泡到行。 */
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { FileEntry } from "../../lib/file-source";
import { actionsFor, type DirState, FileTree } from "./FileTree";

const file = (path: string): FileEntry => ({
	path,
	name: path.split("/").pop() ?? path,
	type: "file",
	size: 1,
	mtime: 0,
	mode: 420,
});
const dir = (path: string): FileEntry => ({
	...file(path),
	type: "dir",
	size: 0,
	mode: 493,
});

const DIRS: Record<string, DirState> = {
	"/r": {
		status: "ok",
		entries: [dir("/r/src"), file("/r/a.txt"), file("/r/b.txt")],
	},
	"/r/src": { status: "ok", entries: [file("/r/src/main.ts")] },
};

function Harness({
	dirtyPaths,
	onOpen,
	onToggle,
}: {
	dirtyPaths: string[];
	onOpen: (path: string) => void;
	onToggle: (path: string) => void;
}) {
	const [expanded, setExpanded] = useState<Record<string, boolean>>({});
	return (
		<FileTree
			rootPath="/r"
			dirs={DIRS}
			expanded={expanded}
			selectedPath={null}
			dirtyPaths={dirtyPaths}
			caps={{ mkdir: true, rename: true, upload: true, download: true }}
			onToggle={(e) => {
				onToggle(e.path);
				setExpanded((x) => ({ ...x, [e.path]: !x[e.path] }));
			}}
			onOpen={(e) => onOpen(e.path)}
			onAction={() => {}}
			onDropFiles={() => {}}
		/>
	);
}

function setup(dirtyPaths: string[] = []) {
	const onOpen = vi.fn();
	const onToggle = vi.fn();
	render(
		<Harness dirtyPaths={dirtyPaths} onOpen={onOpen} onToggle={onToggle} />,
	);
	return { onOpen, onToggle };
}

const item = (name: string) => screen.getByRole("treeitem", { name });
const focused = () => document.activeElement?.getAttribute("data-path");

describe("FileTree 键盘", () => {
	it("整棵树只有一个 Tab 停靠点；↑↓ / Home / End 移动焦点并跟随 tabindex", () => {
		setup();
		const items = screen.getAllByRole("treeitem");
		expect(items.map((i) => i.tabIndex)).toEqual([0, -1, -1]);
		const actions = screen.getAllByRole("button", {
			name: /的操作$/,
		});
		expect(actions.map((b) => b.tabIndex)).toEqual([0, -1, -1]);

		item("src").focus();
		fireEvent.keyDown(item("src"), { key: "ArrowDown" });
		expect(focused()).toBe("/r/a.txt");
		expect(item("a.txt").tabIndex).toBe(0);
		expect(item("src").tabIndex).toBe(-1);
		fireEvent.keyDown(item("a.txt"), { key: "End" });
		expect(focused()).toBe("/r/b.txt");
		fireEvent.keyDown(item("b.txt"), { key: "Home" });
		expect(focused()).toBe("/r/src");
		fireEvent.keyDown(item("src"), { key: "ArrowUp" });
		expect(focused()).toBe("/r/src");
	});

	it("→ 展开目录、再按进入首个子项；← 回到父目录、再按收起；Enter / Space 打开文件", () => {
		const { onOpen, onToggle } = setup();
		item("src").focus();
		fireEvent.keyDown(item("src"), { key: "ArrowRight" });
		expect(onToggle).toHaveBeenLastCalledWith("/r/src");
		expect(item("src")).toHaveAttribute("aria-expanded", "true");
		fireEvent.keyDown(item("src"), { key: "ArrowRight" });
		expect(focused()).toBe("/r/src/main.ts");
		expect(item("main.ts")).toHaveAttribute("aria-level", "2");
		fireEvent.keyDown(item("main.ts"), { key: "Enter" });
		expect(onOpen).toHaveBeenLastCalledWith("/r/src/main.ts");
		fireEvent.keyDown(item("main.ts"), { key: "ArrowLeft" });
		expect(focused()).toBe("/r/src");
		fireEvent.keyDown(item("src"), { key: "ArrowLeft" });
		expect(item("src")).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByRole("treeitem", { name: "main.ts" })).toBeNull();

		item("a.txt").focus();
		fireEvent.keyDown(item("a.txt"), { key: " " });
		expect(onOpen).toHaveBeenLastCalledWith("/r/a.txt");
		fireEvent.keyDown(item("src"), { key: "Enter" });
		expect(onToggle).toHaveBeenCalledTimes(3);
	});

	it("「⋯」按钮上的 Enter / Space 只打开菜单，不打开 / 展开该行", () => {
		const { onOpen, onToggle } = setup();
		const more = screen.getByRole("button", {
			name: i18n.t("files.actions.more", { name: "src" }),
		});
		fireEvent.keyDown(more, { key: "Enter" });
		fireEvent.keyDown(more, { key: " " });
		expect(onToggle).not.toHaveBeenCalled();
		fireEvent.click(more);
		expect(onToggle).not.toHaveBeenCalled();
		expect(onOpen).not.toHaveBeenCalled();
		const menu = screen.getByRole("menu");
		expect(menu.contains(document.activeElement)).toBe(true);
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByRole("menu")).toBeNull();
		expect(focused()).toBe("/r/src");
	});

	it("Shift+F10 在行上打开操作菜单", () => {
		setup();
		item("a.txt").focus();
		fireEvent.keyDown(item("a.txt"), { key: "F10", shiftKey: true });
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	it("有草稿的文件及其所在目录带「未保存」标记", () => {
		setup(["/r/src/main.ts"]);
		const unsaved = i18n.t("files.unsaved");
		expect(
			item("src").querySelector(`[aria-label="${unsaved}"]`),
		).not.toBeNull();
		expect(item("a.txt").querySelector(`[aria-label="${unsaved}"]`)).toBeNull();
	});
});

describe("actionsFor 发布", () => {
	const caps = { mkdir: true, rename: true, upload: true, download: true };
	const file = {
		name: "a.txt",
		path: "/home/user/a.txt",
		type: "file",
	} as FileEntry;
	const dir = { name: "d", path: "/home/user/d", type: "dir" } as FileEntry;
	it("只有开了 publish 的来源、且只对文件提供「发布为公开链接」", () => {
		expect(actionsFor(file, caps)).not.toContain("publish");
		expect(actionsFor(file, { ...caps, publish: true })).toContain("publish");
		expect(actionsFor(dir, { ...caps, publish: true })).not.toContain(
			"publish",
		);
	});
});
