/**
 * 设置 → Agent → 命令与规则：四类条目各自的启停键（规则 `.mdc`、instruction 去 `.instructions.md`）；
 * 新建命令写出 description / argument-hint；规则的 globs 写成列表、alwaysApply 写 true。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { parseFrontmatter } from "../../../lib/frontmatter";
import {
	agentFetch,
	home,
	lastDisabled,
	renderPage,
	resetHome,
} from "./agent-config-mock";
import CollectionsPage from "./CollectionsPage";

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

beforeEach(() => {
	resetHome({
		"config.yml": "disabledExtensions: [prompt:old]\n",
		"commands/ship.md": "---\ndescription: 发布\n---\n执行发布流程\n",
		"rules/style.mdc": '---\nglobs: ["*.ts"]\n---\n用 tab 缩进\n',
		"prompts/old.md": "旧提示\n",
		"instructions/py.instructions.md":
			'---\napplyTo: "**/*.py"\n---\n用 ruff\n',
	});
	vi.stubGlobal("fetch", agentFetch);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

async function toggle(name: string) {
	const sw = await screen.findByRole("switch", { name: `启用 ${name}` });
	await waitFor(() => expect(sw).toBeEnabled());
	fireEvent.click(sw);
	return sw;
}

describe("CollectionsPage", () => {
	it("各类条目的启停键：slash-command / rule / prompt / instruction", async () => {
		renderPage(<CollectionsPage />);
		expect(await screen.findByText("ship")).toBeInTheDocument();
		// 命令没有 frontmatter 描述时取正文首行；这里有描述
		expect(screen.getByText("发布")).toBeInTheDocument();
		expect(screen.getByText(".mdc")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "启用 old" })).not.toBeChecked();

		await toggle("ship");
		await waitFor(() =>
			expect(lastDisabled()).toEqual(["prompt:old", "slash-command:ship"]),
		);
		await toggle("style");
		await waitFor(() =>
			expect(lastDisabled()).toEqual([
				"prompt:old",
				"slash-command:ship",
				"rule:style",
			]),
		);
		await toggle("py");
		await waitFor(() =>
			expect(lastDisabled()).toEqual([
				"prompt:old",
				"slash-command:ship",
				"rule:style",
				"instruction:py",
			]),
		);
		await toggle("old");
		await waitFor(() =>
			expect(lastDisabled()).toEqual([
				"slash-command:ship",
				"rule:style",
				"instruction:py",
			]),
		);
	});

	it("新建命令：description 与 argument-hint 写进 frontmatter", async () => {
		renderPage(<CollectionsPage />);
		await screen.findByText("ship");
		const card = screen.getByText("斜杠命令").closest("section") as HTMLElement;
		fireEvent.click(within(card).getByRole("button", { name: "新建" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText(/^名称/), {
			target: { value: "fix" },
		});
		fireEvent.change(within(dialog).getByLabelText("描述"), {
			target: { value: "修复问题" },
		});
		fireEvent.change(within(dialog).getByLabelText(/^参数提示/), {
			target: { value: "<issue>" },
		});
		fireEvent.change(
			await within(dialog).findByLabelText("editor:commands/fix.md"),
			{ target: { value: "修复 $ARGUMENTS\n" } },
		);
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(home.puts[0]?.path).toBe("commands/fix.md");
		const parsed = parseFrontmatter(home.puts[0]?.body ?? "");
		expect(parsed.data).toEqual({
			description: "修复问题",
			"argument-hint": "<issue>",
		});
		expect(parsed.body).toBe("修复 $ARGUMENTS\n");
	});

	it("编辑规则：globs 写成列表，alwaysApply 打开写 true", async () => {
		renderPage(<CollectionsPage />);
		fireEvent.click(await screen.findByRole("button", { name: "编辑 style" }));
		const dialog = await screen.findByRole("dialog");
		const globs = within(dialog).getByLabelText(/^文件匹配/);
		expect(globs).toHaveValue("*.ts");
		fireEvent.change(globs, { target: { value: "*.ts, src/**/*.tsx" } });
		fireEvent.click(within(dialog).getByRole("switch", { name: "始终应用" }));
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(home.puts[0]?.path).toBe("rules/style.mdc");
		expect(parseFrontmatter(home.puts[0]?.body ?? "").data).toEqual({
			globs: ["*.ts", "src/**/*.tsx"],
			alwaysApply: true,
		});
	});
});
