/**
 * 设置 → Agent → 子 agent：停用写 `task.disabledAgents`；模型覆盖写 `task.agentModelOverrides`；
 * 内置 agent 只读（没有编辑 / 删除）；新建写出 Core 认的 frontmatter；
 * 编辑期间文件被外部修改时首次保存冲突、再点保存仍不覆盖，载入最新内容后才能保存；头像写 agent 头像接口。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { parseFrontmatter } from "../../../lib/frontmatter";
import { readConfigDoc } from "../../../lib/omp-config";
import { pickAvatarGlyph } from "../../../test/glyph-picker";
import { agentFetch, home, renderPage, resetHome } from "./agent-config-mock";
import SubagentsPage, { agentForm, applyAgentForm } from "./SubagentsPage";

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

const FOO =
	"---\nname: foo\ndescription: 示例 agent\nmodel: '@smol'\n---\n\n你是 foo。\n";

let avatarPuts: unknown[] = [];

/** 在 omp-home 文件接口之外补上头像、role 列表与模型目录。 */
const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const url = new URL(req.url, "http://x");
		if (url.pathname === "/api/v1/settings/agent-avatars") {
			if (req.method === "PUT") {
				const body = (await req.json()) as { name: string };
				avatarPuts.push(body);
				return Response.json({ ...body, source: "bundled" });
			}
			return Response.json({
				agents: [
					{ name: "task", source: "bundled" },
					{ name: "scout", source: "bundled" },
					{ name: "foo", source: "user", description: "示例 agent" },
				],
			});
		}
		if (url.pathname === "/api/v1/settings/role-identities")
			return Response.json({
				roles: [
					{ role: "smol", inCycle: true },
					{ role: "default", inCycle: true },
				],
			});
		if (url.pathname === "/api/v1/models/catalog")
			return Response.json({ models: [], fetchedAt: 1, stale: false });
		return agentFetch(input, init);
	},
);

function lastConfig(): Record<string, unknown> | undefined {
	const put = home.puts.filter((p) => p.path === "config.yml").at(-1);
	return put ? readConfigDoc(put.body).toJS() : undefined;
}

beforeEach(() => {
	resetHome({ "agents/foo.md": FOO });
	avatarPuts = [];
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("SubagentsPage", () => {
	it("标注作用范围；停用 foo：PUT config.yml，task.disabledAgents 含 foo", async () => {
		renderPage(<SubagentsPage />);
		expect(
			screen.getByText("保存后下发到 orb，不作用于全局助手。"),
		).toBeInTheDocument();
		const sw = await screen.findByRole("checkbox", { name: "启用 foo" });
		await waitFor(() => expect(sw).toBeEnabled());
		fireEvent.click(sw);
		await waitFor(() =>
			expect(lastConfig()).toEqual({ task: { disabledAgents: ["foo"] } }),
		);
	});

	it("内置 agent 只读：显示简介、没有编辑与删除；可设模型覆盖", async () => {
		renderPage(<SubagentsPage />);
		const row = (await screen.findByText("task")).closest("li");
		if (!row) throw new Error("no task row");
		expect(row).toHaveTextContent("通用子 agent");
		expect(within(row).queryByRole("button", { name: "编辑 task" })).toBeNull();
		expect(within(row).queryByRole("button", { name: "删除 task" })).toBeNull();
		await waitFor(() =>
			expect(
				within(row).getByRole("checkbox", { name: "启用 task" }),
			).toBeEnabled(),
		);
		fireEvent.click(
			within(row).getByRole("button", { name: "设置 task 的模型与头像" }),
		);
		const input = within(row).getByLabelText("为 task 添加覆盖模型");
		fireEvent.change(input, { target: { value: "openai/gpt-5" } });
		fireEvent.keyDown(input, { key: "Enter" });
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				task: { agentModelOverrides: { task: "openai/gpt-5" } },
			}),
		);
		// 再加一个：写成按顺序回退的列表
		const again = within(row).getByLabelText("为 task 添加覆盖模型");
		fireEvent.change(again, { target: { value: "@slow" } });
		fireEvent.keyDown(again, { key: "Enter" });
		await waitFor(() =>
			expect(lastConfig()).toEqual({
				task: { agentModelOverrides: { task: ["openai/gpt-5", "@slow"] } },
			}),
		);
	});

	it("头像：选字形写 agent 头像接口", async () => {
		renderPage(<SubagentsPage />);
		const row = (await screen.findByText("scout")).closest("li");
		if (!row) throw new Error("no scout row");
		fireEvent.click(
			within(row).getByRole("button", { name: "设置 scout 的模型与头像" }),
		);
		pickAvatarGlyph(row, "🦊");
		await waitFor(() =>
			expect(avatarPuts).toEqual([{ name: "scout", avatarIcon: "🦊" }]),
		);
	});

	it("新建：写出 name、description、model、thinkingLevel、blocking 等 frontmatter", async () => {
		renderPage(<SubagentsPage />);
		await screen.findByText("foo");
		fireEvent.click(screen.getByRole("button", { name: "新建" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText(/^名称/), {
			target: { value: "bar" },
		});
		fireEvent.change(within(dialog).getByLabelText(/^描述/), {
			target: { value: "审查 diff" },
		});
		const model = within(dialog).getByLabelText("添加模型");
		fireEvent.change(model, { target: { value: "@smol" } });
		fireEvent.keyDown(model, { key: "Enter" });
		fireEvent.change(within(dialog).getByLabelText("思考档位"), {
			target: { value: "high" },
		});
		fireEvent.change(within(dialog).getByLabelText(/^工具/), {
			target: { value: "read, grep" },
		});
		fireEvent.click(within(dialog).getByRole("checkbox", { name: "阻塞运行" }));
		fireEvent.change(within(dialog).getByLabelText("editor:agents/bar.md"), {
			target: { value: "你是 bar。\n" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(home.puts[0]?.path).toBe("agents/bar.md");
		const { data, body } = parseFrontmatter(home.puts[0]?.body ?? "");
		expect(data).toEqual({
			name: "bar",
			description: "审查 diff",
			model: "@smol",
			thinkingLevel: "high",
			tools: ["read", "grep"],
			blocking: true,
		});
		expect(body).toBe("你是 bar。\n");
	});

	it("新建：main / sub 是保留名，已有名字不能重复", async () => {
		renderPage(<SubagentsPage />);
		await screen.findByText("foo");
		fireEvent.click(screen.getByRole("button", { name: "新建" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText(/^描述/), {
			target: { value: "x" },
		});
		const name = within(dialog).getByLabelText(/^名称/);
		fireEvent.change(name, { target: { value: "Main" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		expect(
			await within(dialog).findByText("main 与 sub 是保留名称。"),
		).toBeInTheDocument();
		fireEvent.change(name, { target: { value: "task" } });
		expect(within(dialog).getByText("已有同名条目。")).toBeInTheDocument();
		expect(home.puts).toHaveLength(0);
	});

	it("编辑期间文件被外部修改：首次保存冲突，再点保存仍不覆盖；载入最新内容后才能保存", async () => {
		renderPage(<SubagentsPage />);
		fireEvent.click(await screen.findByRole("button", { name: "编辑 foo" }));
		const dialog = await screen.findByRole("dialog");
		await waitFor(() =>
			expect(within(dialog).getByLabelText(/^描述/)).toHaveValue("示例 agent"),
		);
		fireEvent.change(within(dialog).getByLabelText(/^描述/), {
			target: { value: "我的旧稿" },
		});
		const external = "---\nname: foo\ndescription: 别处改的\n---\n\n新正文。\n";
		home.files["agents/foo.md"] = external;

		const save = () =>
			fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		save();
		expect(
			await within(dialog).findByText(/打开后文件已在别处修改/),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText(/^描述/)).toHaveValue("我的旧稿");
		save();
		await waitFor(() =>
			expect(
				within(dialog).getByRole("button", { name: "保存" }),
			).toBeEnabled(),
		);
		expect(home.puts).toHaveLength(0);
		expect(home.files["agents/foo.md"]).toBe(external);

		fireEvent.click(
			within(dialog).getByRole("button", {
				name: "载入最新内容（放弃我的改动）",
			}),
		);
		const reloaded = await screen.findByRole("dialog");
		await waitFor(() =>
			expect(within(reloaded).getByLabelText(/^描述/)).toHaveValue("别处改的"),
		);
		fireEvent.change(within(reloaded).getByLabelText(/^描述/), {
			target: { value: "合并后" },
		});
		fireEvent.click(within(reloaded).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(home.puts[0]?.body).toBe(
			"---\nname: foo\ndescription: 合并后\n---\n\n新正文。\n",
		);
	});

	it("删除：确认后删文件，并把它从 task.disabledAgents 与模型覆盖里清掉", async () => {
		home.files["config.yml"] =
			"task:\n  disabledAgents: [foo, other]\n  agentModelOverrides:\n    foo: openai/gpt-5\n";
		renderPage(<SubagentsPage />);
		fireEvent.click(await screen.findByRole("button", { name: "删除 foo" }));
		const dialog = await screen.findByRole("dialog");
		fireEvent.click(within(dialog).getByRole("button", { name: "删除" }));
		await waitFor(() => expect(home.deletes).toEqual(["agents/foo.md"]));
		await waitFor(() =>
			expect(lastConfig()).toEqual({ task: { disabledAgents: ["other"] } }),
		);
	});
});

describe("agentForm / applyAgentForm", () => {
	it("往返保留未列出的键；advisor / prewalk 区分默认与指定模型；旧键 thinking 沿用", () => {
		const data = {
			name: "x",
			description: "d",
			thinking: "low",
			spawns: "*",
			advisor: true,
			prewalk: "@smol",
			output: { type: "object" },
		};
		const form = agentForm(data);
		expect(form).toMatchObject({
			thinkingLevel: "low",
			spawns: "*",
			advisor: { mode: "default" },
			prewalk: { mode: "custom", pattern: "@smol" },
		});
		expect(applyAgentForm(data, form)).toEqual(data);
		expect(
			applyAgentForm(data, {
				...form,
				thinkingLevel: "",
				advisor: { mode: "off", pattern: "" },
				model: ["a/b", "@slow"],
			}),
		).toEqual({
			name: "x",
			description: "d",
			spawns: "*",
			prewalk: "@smol",
			output: { type: "object" },
			model: ["a/b", "@slow"],
		});
	});
});
