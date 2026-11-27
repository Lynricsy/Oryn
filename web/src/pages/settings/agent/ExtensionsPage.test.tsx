/**
 * 设置 → Agent → 扩展、hook 与工具：名称推导对照 Core（扩展模块的文件 / index / package.json 声明，hook 键，
 * 工具 json / md 里的 name）；启停写对应键；需重启的提示；行内源码编辑与新建文件。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { strToU8, zipSync } from "fflate";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	agentFetch,
	generativeUi,
	home,
	lastDisabled,
	renderPage,
	resetGenerativeUi,
	resetHome,
} from "./agent-config-mock";
import ExtensionsPage, {
	extensionEntries,
	hookEntries,
	toolEntries,
} from "./ExtensionsPage";

vi.mock("../../../components/editor/CodeEditor", () => ({
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

const FILES: Record<string, string> = {
	"extensions/notify.ts": "export default () => {};\n",
	"extensions/README.md": "说明\n",
	"extensions/guard/index.ts": "export default () => {};\n",
	"extensions/guard/util.ts": "export {};\n",
	"extensions/pkg/package.json": JSON.stringify({
		omp: { extensions: ["./src/main.ts", "./lib"] },
	}),
	"extensions/pkg/src/main.ts": "",
	"extensions/pkg/lib/index.js": "",
	"extensions/empty/notes.txt": "",
	"hooks/pre/bash.ts": "",
	"hooks/post/*.sh": "",
	"hooks/pre/.skip.ts": "",
	"tools/weather.json": JSON.stringify({ name: "get_weather" }),
	"tools/search.md": "---\nname: web_search2\n---\n",
	"tools/run.py": "",
	"tools/kit/index.ts": "",
	"tools/notes.txt": "",
};

beforeEach(() => {
	resetHome(FILES);
	resetGenerativeUi();
	vi.stubGlobal("fetch", agentFetch);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

const texts = new Map(Object.entries(FILES));
const paths = Object.keys(FILES);

describe("名称推导", () => {
	it("扩展模块：顶层 ts/js、index 子目录、package.json 声明；不会加载的子目录没有键", () => {
		expect(
			extensionEntries(paths, texts).map((e) => [e.name, e.key ?? null]),
		).toEqual([
			["empty", null],
			["guard", "extension-module:guard"],
			["notify", "extension-module:notify"],
			["lib", "extension-module:lib"],
			["main", "extension-module:main"],
		]);
	});

	it("hook：pre / post 下的文件，跳过点文件", () => {
		expect(hookEntries(paths).map((e) => e.key)).toEqual([
			"hook:post:*:*.sh",
			"hook:pre:bash:bash.ts",
		]);
	});

	it("工具：json 的 name、md 的 frontmatter name、其余文件名；index.ts 子目录", () => {
		expect(toolEntries(paths, texts).map((e) => e.key)).toEqual([
			"tool:kit",
			"tool:run",
			"tool:web_search2",
			"tool:get_weather",
		]);
	});
});

describe("ExtensionsPage", () => {
	it("生成式 UI 开关：读出当前值，切换时先显示新值再写入", async () => {
		renderPage(<ExtensionsPage />);
		const toggle = await screen.findByRole("switch", {
			name: "在对话里给出视图",
		});
		expect(toggle).toBeChecked();
		fireEvent.click(toggle);
		await waitFor(() => expect(toggle).not.toBeChecked());
		await waitFor(() => expect(generativeUi.puts).toEqual([false]));
		await waitFor(() => expect(generativeUi.enabled).toBe(false));
		expect(toggle).not.toBeChecked();
	});

	it("生成式 UI 开关：写入失败时回滚", async () => {
		generativeUi.fail = true;
		renderPage(<ExtensionsPage />);
		const toggle = await screen.findByRole("switch", {
			name: "在对话里给出视图",
		});
		fireEvent.click(toggle);
		await waitFor(() => expect(generativeUi.puts).toEqual([false]));
		await waitFor(() => expect(toggle).toBeChecked());
		expect(generativeUi.enabled).toBe(true);
	});

	it("提示需重启；启停写扩展模块、hook、工具的键", async () => {
		renderPage(<ExtensionsPage />);
		expect(
			screen.getByText("扩展、hook 与自定义工具在会话重启后才生效。"),
		).toBeInTheDocument();
		expect(
			screen.getByText("保存后下发到 orb，不作用于全局助手。"),
		).toBeInTheDocument();
		const notify = await screen.findByRole("switch", { name: "启用 notify" });
		await waitFor(() => expect(notify).toBeEnabled());
		fireEvent.click(notify);
		await waitFor(() =>
			expect(lastDisabled()).toEqual(["extension-module:notify"]),
		);
		fireEvent.click(screen.getByRole("switch", { name: "启用 bash.ts" }));
		await waitFor(() =>
			expect(lastDisabled()).toEqual([
				"extension-module:notify",
				"hook:pre:bash:bash.ts",
			]),
		);
		fireEvent.click(
			await screen.findByRole("switch", { name: "启用 get_weather" }),
		);
		await waitFor(() =>
			expect(lastDisabled()).toEqual([
				"extension-module:notify",
				"hook:pre:bash:bash.ts",
				"tool:get_weather",
			]),
		);
		expect(screen.queryByRole("switch", { name: "启用 empty" })).toBeNull();
		expect(screen.getByText("不会加载")).toBeInTheDocument();
	});

	it("目录式条目打开文件管理：切换文件不丢草稿，保存只写当前文件", async () => {
		renderPage(<ExtensionsPage />);
		fireEvent.click(
			await screen.findByRole("button", { name: "编辑 guard 的源码" }),
		);
		const dialog = await screen.findByRole("dialog");
		const index = await within(dialog).findByLabelText(
			"editor:extensions/guard/index.ts",
		);
		expect(index).toHaveValue("export default () => {};\n");
		fireEvent.change(index, { target: { value: "// 草稿\n" } });
		fireEvent.click(within(dialog).getByRole("button", { name: /^util\.ts/ }));
		const util = await within(dialog).findByLabelText(
			"editor:extensions/guard/util.ts",
		);
		fireEvent.change(util, { target: { value: "export const x = 1;\n" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(home.puts).toEqual([
				{ path: "extensions/guard/util.ts", body: "export const x = 1;\n" },
			]),
		);
		fireEvent.click(within(dialog).getByRole("button", { name: /^index\.ts/ }));
		expect(
			await within(dialog).findByLabelText("editor:extensions/guard/index.ts"),
		).toHaveValue("// 草稿\n");
		// 入口文件不能单独删除
		expect(
			within(dialog).getByRole("button", {
				name: "入口文件随条目一起删除，不能单独删",
			}),
		).toBeDisabled();
	});

	it("新建目录式工具：入口为 index.ts", async () => {
		renderPage(<ExtensionsPage />);
		await screen.findByText("bash.ts");
		fireEvent.click(
			screen.getByRole("button", { name: "在「自定义工具」中新建" }),
		);
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(within(dialog).getByRole("combobox", { name: /^形式/ }));
		fireEvent.click(await screen.findByRole("option", { name: /^目录/ }));
		const name = within(dialog).getByLabelText(/^目录名/);
		fireEvent.change(name, { target: { value: "kit" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "继续" }));
		expect(name).toHaveAttribute("aria-invalid", "true");
		fireEvent.change(name, { target: { value: "deploy" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "继续" }));
		const editor = await screen.findByLabelText("editor:tools/deploy/index.ts");
		fireEvent.change(editor, { target: { value: "export default {};\n" } });
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(home.puts).toEqual([
				{ path: "tools/deploy/index.ts", body: "export default {};\n" },
			]),
		);
	});

	it("导入工具目录：zip 只有一个顶层文件夹时以它为名，整目录一次写入", async () => {
		renderPage(<ExtensionsPage />);
		await screen.findByText("bash.ts");
		fireEvent.click(screen.getByRole("button", { name: "导入自定义工具" }));
		const dialog = await screen.findByRole("dialog");
		const zip = zipSync({
			"weather/index.ts": strToU8("export default {};\n"),
			"weather/data/icon.png": new Uint8Array([137, 80, 78, 71, 0, 1]),
			"__MACOSX/weather/._index.ts": strToU8("junk"),
		});
		fireEvent.change(within(dialog).getByTestId("pick-zip-input"), {
			target: { files: [new File([zip], "weather-v2.zip")] },
		});
		expect(await within(dialog).findByDisplayValue("weather")).toBeVisible();
		fireEvent.click(within(dialog).getByRole("button", { name: "导入" }));
		await waitFor(() => expect(home.batches).toHaveLength(1));
		expect(home.batches[0]).toEqual({
			files: [
				{
					path: "tools/weather/data/icon.png",
					content: btoa(String.fromCharCode(137, 80, 78, 71, 0, 1)),
					encoding: "base64",
				},
				{
					path: "tools/weather/index.ts",
					content: "export default {};\n",
					encoding: "utf8",
				},
			],
		});
	});

	it("新建 hook：选 post、填文件名，进编辑器保存", async () => {
		renderPage(<ExtensionsPage />);
		await screen.findByText("bash.ts");
		fireEvent.click(screen.getByRole("button", { name: "在「Hook」中新建" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(within(dialog).getByRole("combobox", { name: /^时机/ }));
		fireEvent.click(await screen.findByRole("option", { name: /post/i }));
		const name = within(dialog).getByLabelText(/^文件名/);
		fireEvent.change(name, { target: { value: "bad name" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "继续" }));
		expect(name).toHaveAttribute("aria-invalid", "true");
		fireEvent.change(name, { target: { value: "edit.ts" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "继续" }));
		const editor = await screen.findByLabelText("editor:hooks/post/edit.ts");
		fireEvent.change(editor, { target: { value: "// hook\n" } });
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(home.puts).toEqual([
				{ path: "hooks/post/edit.ts", body: "// hook\n" },
			]),
		);
	});
});
