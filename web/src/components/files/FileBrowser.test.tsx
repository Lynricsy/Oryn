import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import i18n from "../../i18n";
import {
	baseName,
	type FileEntry,
	type FileSource,
	parentPath,
} from "../../lib/file-source";
import { FileBrowser } from "./FileBrowser";

// 编辑器换成 textarea：这里测浏览器的流程，CodeMirror 本身另有测试
vi.mock("../editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange?(v: string): void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange?.(e.target.value)}
		/>
	),
}));

const t = (k: string, o: Record<string, unknown> = {}) =>
	i18n.t(k as "common.save", o);

/** 内存文件来源：根 `/r`，etag = 版本号。 */
function memorySource() {
	const files = new Map<string, { text: string; v: number }>();
	const dirs = new Set<string>(["/r"]);
	const entryOf = (path: string): FileEntry => {
		const f = files.get(path);
		if (f)
			return {
				path,
				name: baseName(path),
				type: "file",
				size: f.text.length,
				mtime: f.v,
				mode: 420,
				etag: `"${f.v}"`,
				mime: "text/plain",
				text: true,
			};
		if (dirs.has(path))
			return {
				path,
				name: baseName(path),
				type: "dir",
				size: 0,
				mtime: 0,
				mode: 493,
			};
		throw new ApiError(404, "not_found", path);
	};
	const src: FileSource = {
		id: "mem",
		caps: { mkdir: true, rename: true, upload: true, download: false },
		roots: async () => [{ label: "files.roots.workspace", path: "/r" }],
		list: async (dir) =>
			[...dirs, ...files.keys()]
				.filter((p) => p !== dir && parentPath(p) === dir)
				.sort()
				.map(entryOf),
		stat: async (p) => entryOf(p),
		readText: async (p) => {
			const f = files.get(p);
			if (!f) throw new ApiError(404, "not_found", p);
			return { text: f.text, etag: `"${f.v}"`, truncated: false };
		},
		readBlob: async (p) => new Blob([files.get(p)?.text ?? ""]),
		write: async (p, body, opts = {}) => {
			const cur = files.get(p);
			if (cur && opts.overwrite === false) throw new ApiError(409, "exists", p);
			if (opts.etag && (!cur || `"${cur.v}"` !== opts.etag))
				throw new ApiError(412, "modified", p);
			const text = typeof body === "string" ? body : await body.text();
			files.set(p, { text, v: (cur?.v ?? 0) + 1 });
			return entryOf(p);
		},
		mkdir: async (p) => {
			dirs.add(p);
		},
		rename: async (from, to) => {
			const f = files.get(from);
			if (!f) throw new ApiError(404, "not_found", from);
			files.delete(from);
			files.set(to, f);
		},
		remove: async (p) => {
			files.delete(p);
			dirs.delete(p);
		},
		downloadUrl: () => null,
	};
	return { src, files, dirs };
}

let mem: ReturnType<typeof memorySource>;

function setup() {
	render(<FileBrowser sources={[mem.src]} storageKey="test" />);
}

async function openMenu(name: string) {
	fireEvent.click(
		await screen.findByRole("button", {
			name: t("files.actions.more", { name }),
		}),
	);
	return screen.getByRole("menu");
}

describe("FileBrowser", () => {
	beforeEach(() => {
		localStorage.clear();
		mem = memorySource();
		mem.files.set("/r/a.txt", { text: "alpha", v: 1 });
	});

	it("新建文件：写入空文件（overwrite=false）并在编辑器中打开", async () => {
		const write = vi.spyOn(mem.src, "write");
		setup();
		await screen.findByText("a.txt");
		fireEvent.click(
			screen.getByRole("button", { name: t("files.actions.newFile") }),
		);
		const dialog = screen.getByRole("dialog");
		fireEvent.change(within(dialog).getByRole("textbox"), {
			target: { value: "notes.md" },
		});
		fireEvent.click(
			within(dialog).getByRole("button", { name: t("common.create") }),
		);
		expect(await screen.findByText("notes.md")).toBeInTheDocument();
		expect(write).toHaveBeenCalledWith("/r/notes.md", "", { overwrite: false });
		expect(await screen.findByLabelText("editor:/r/notes.md")).toHaveValue("");
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	it("新建文件重名：显示 exists 错误，不覆盖已有内容", async () => {
		setup();
		await screen.findByText("a.txt");
		fireEvent.click(
			screen.getByRole("button", { name: t("files.actions.newFile") }),
		);
		const dialog = screen.getByRole("dialog");
		fireEvent.change(within(dialog).getByRole("textbox"), {
			target: { value: "a.txt" },
		});
		fireEvent.click(
			within(dialog).getByRole("button", { name: t("common.create") }),
		);
		expect(await within(dialog).findByRole("alert")).toBeInTheDocument();
		expect(mem.files.get("/r/a.txt")?.text).toBe("alpha");
	});

	it("名称含 .. 时禁止提交", async () => {
		setup();
		await screen.findByText("a.txt");
		fireEvent.click(
			screen.getByRole("button", { name: t("files.actions.newFolder") }),
		);
		const dialog = screen.getByRole("dialog");
		fireEvent.change(within(dialog).getByRole("textbox"), {
			target: { value: "../x" },
		});
		expect(
			within(dialog).getByRole("button", { name: t("common.create") }),
		).toBeDisabled();
	});

	it("重命名：菜单 → 输入新名 → 树里只剩新名，已打开的文件跟随新路径", async () => {
		setup();
		fireEvent.click(await screen.findByText("a.txt"));
		await screen.findByLabelText("editor:/r/a.txt");
		const menu = await openMenu("a.txt");
		fireEvent.click(
			within(menu).getByRole("menuitem", { name: t("files.actions.rename") }),
		);
		const dialog = screen.getByRole("dialog");
		const input = within(dialog).getByRole("textbox");
		expect(input).toHaveValue("a.txt");
		fireEvent.change(input, { target: { value: "b.txt" } });
		fireEvent.click(
			within(dialog).getByRole("button", { name: t("files.actions.rename") }),
		);
		expect(await screen.findByText("b.txt")).toBeInTheDocument();
		expect(screen.queryByText("a.txt")).toBeNull();
		expect(mem.files.has("/r/b.txt")).toBe(true);
		expect(await screen.findByLabelText("editor:/r/b.txt")).toHaveValue(
			"alpha",
		);
	});

	it("删除：确认后删除并关闭已打开的文件", async () => {
		setup();
		fireEvent.click(await screen.findByText("a.txt"));
		await screen.findByLabelText("editor:/r/a.txt");
		const menu = await openMenu("a.txt");
		fireEvent.click(
			within(menu).getByRole("menuitem", { name: t("files.actions.delete") }),
		);
		const dialog = screen.getByRole("dialog");
		expect(mem.files.has("/r/a.txt")).toBe(true);
		fireEvent.click(
			within(dialog).getByRole("button", { name: t("common.delete") }),
		);
		await waitFor(() => expect(screen.queryByText("a.txt")).toBeNull());
		expect(mem.files.has("/r/a.txt")).toBe(false);
		expect(screen.queryByLabelText("editor:/r/a.txt")).toBeNull();
		expect(screen.getByText(t("files.pick"))).toBeInTheDocument();
	});

	it("保存带 etag；被他人修改（412）→ 覆盖", async () => {
		setup();
		fireEvent.click(await screen.findByText("a.txt"));
		const editor = await screen.findByLabelText("editor:/r/a.txt");
		fireEvent.change(editor, { target: { value: "mine" } });
		mem.files.set("/r/a.txt", { text: "theirs", v: 7 });
		fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
		const dialog = await screen.findByRole("dialog", {
			name: t("files.conflict.title"),
		});
		expect(mem.files.get("/r/a.txt")?.text).toBe("theirs");
		fireEvent.click(
			within(dialog).getByRole("button", {
				name: t("files.conflict.overwrite"),
			}),
		);
		await waitFor(() => expect(mem.files.get("/r/a.txt")?.text).toBe("mine"));
		expect(screen.queryByRole("dialog")).toBeNull();
		// 覆盖后拿到新 etag：再改再存不再冲突
		fireEvent.change(screen.getByLabelText("editor:/r/a.txt"), {
			target: { value: "mine 2" },
		});
		fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
		await waitFor(() => expect(mem.files.get("/r/a.txt")?.text).toBe("mine 2"));
	});

	it("412 → 重新加载：丢弃草稿并显示最新内容", async () => {
		setup();
		fireEvent.click(await screen.findByText("a.txt"));
		fireEvent.change(await screen.findByLabelText("editor:/r/a.txt"), {
			target: { value: "mine" },
		});
		mem.files.set("/r/a.txt", { text: "theirs", v: 7 });
		fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
		const dialog = await screen.findByRole("dialog", {
			name: t("files.conflict.title"),
		});
		fireEvent.click(
			within(dialog).getByRole("button", { name: t("files.conflict.reload") }),
		);
		await waitFor(() =>
			expect(screen.getByLabelText("editor:/r/a.txt")).toHaveValue("theirs"),
		);
		expect(mem.files.get("/r/a.txt")?.text).toBe("theirs");
	});

	it("上传重名：先 overwrite=false，收到 409 后询问，选「替换」再覆盖写入", async () => {
		const write = vi.spyOn(mem.src, "write");
		setup();
		await screen.findByText("a.txt");
		const input = screen.getByTestId("file-upload-input");
		fireEvent.change(input, {
			target: {
				files: [new File(["new bytes"], "a.txt"), new File(["c"], "c.txt")],
			},
		});
		const dialog = await screen.findByRole("dialog", {
			name: t("files.upload.conflictTitle"),
		});
		// 不冲突的文件照常写入；冲突的文件在作答前保持原样
		await waitFor(() => expect(mem.files.get("/r/c.txt")?.text).toBe("c"));
		expect(mem.files.get("/r/a.txt")?.text).toBe("alpha");
		fireEvent.click(
			within(dialog).getByRole("button", { name: t("files.upload.replace") }),
		);
		await waitFor(() =>
			expect(mem.files.get("/r/a.txt")?.text).toBe("new bytes"),
		);
		const aWrites = write.mock.calls.filter((c) => c[0] === "/r/a.txt");
		expect(aWrites.map((c) => c[2]?.overwrite)).toEqual([false, true]);
	});

	it("超过 512MiB 的文件在客户端直接拒绝，不发请求", async () => {
		const write = vi.spyOn(mem.src, "write");
		setup();
		await screen.findByText("a.txt");
		const huge = new File(["x"], "huge.bin");
		Object.defineProperty(huge, "size", { value: 512 * 1024 * 1024 + 1 });
		fireEvent.change(screen.getByTestId("file-upload-input"), {
			target: { files: [huge] },
		});
		expect(
			await screen.findByText(t("files.upload.status.rejected")),
		).toBeInTheDocument();
		expect(write).not.toHaveBeenCalled();
	});

	it("文件夹懒加载：展开时才 list", async () => {
		mem.dirs.add("/r/sub");
		mem.files.set("/r/sub/deep.txt", { text: "d", v: 1 });
		const list = vi.spyOn(mem.src, "list");
		setup();
		fireEvent.click(await screen.findByText("sub"));
		expect(await screen.findByText("deep.txt")).toBeInTheDocument();
		expect(list.mock.calls.map((c) => c[0])).toEqual(["/r", "/r/sub"]);
	});
});
