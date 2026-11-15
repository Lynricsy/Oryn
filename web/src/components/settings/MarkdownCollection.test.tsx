/**
 * MarkdownCollection：从扁平文件列表找条目（技能目录式 / 平铺 md、mdc），名称与描述取 frontmatter；
 * 开关写 config.yml 的 `disabledExtensions`（保留注释）；新建（名称校验、frontmatter + 正文）、编辑（保留未列出的键）、
 * 删除（确认，目录整删，顺带清掉停用键）；config.yml 在别处被改过时不写入并提示。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { skillKey } from "../../lib/omp-config";
import {
	agentFetch,
	home,
	lastDisabled,
	renderPage,
	resetHome,
} from "../../pages/settings/agent/agent-config-mock";
import { collectionEntries, MarkdownCollection } from "./MarkdownCollection";

// 编辑器换成 textarea：这里测接线，CodeMirror 本身另有测试
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

const SKILL_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/;

function mountSkills() {
	return renderPage(
		<MarkdownCollection
			dir="skills"
			layout="dir-skill"
			keyOf={skillKey}
			fields={[
				{
					key: "description",
					kind: "textarea",
					label: "描述",
					required: true,
				},
			]}
			title="技能"
			icon="sparkle"
			emptyText="还没有技能。"
			namePattern={SKILL_PATTERN}
			requireDescription
		/>,
	);
}

beforeEach(() => {
	resetHome({
		"config.yml": "# 我的配置\ntheme: dark # 深色\n",
		"skills/review/SKILL.md":
			"---\nname: code-review\ndescription: 审查改动\nhide: true\n---\n先读 diff。\n",
		"skills/review/scripts/run.sh": "echo hi\n",
		"skills/draft/SKILL.md": "没有 frontmatter\n",
		"skills/.hidden/SKILL.md": "---\ndescription: x\n---\n",
	});
	vi.stubGlobal("fetch", agentFetch);
	agentFetch.mockClear();
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("collectionEntries", () => {
	it("平铺目录按扩展名过滤，跳过子目录与点文件", () => {
		const files = [
			"rules/a.md",
			"rules/b.mdc",
			"rules/c.txt",
			"rules/.d.md",
			"rules/sub/e.md",
			"commands/x.md",
		].map((path) => ({ path, size: 1, mode: 420, sha256: "", updatedAt: 1 }));
		expect(
			collectionEntries(files, "rules", "flat-md", ["md", "mdc"]).map((e) => [
				e.path,
				e.stem,
			]),
		).toEqual([
			["rules/a.md", "a"],
			["rules/b.mdc", "b"],
		]);
	});
});

describe("MarkdownCollection（技能）", () => {
	it("列出条目：名称取 frontmatter 否则目录名，缺描述的标出", async () => {
		mountSkills();
		expect(await screen.findByText("code-review")).toBeInTheDocument();
		expect(screen.getByText("审查改动")).toBeInTheDocument();
		expect(screen.getByText("draft")).toBeInTheDocument();
		expect(screen.getByText("缺少描述，不会加载")).toBeInTheDocument();
		expect(screen.queryByText(".hidden")).toBeNull();
	});

	it("停用：config.yml 的 disabledExtensions 加入 skill:<名称>，注释保留；再启用则移除", async () => {
		mountSkills();
		const sw = await screen.findByRole("checkbox", {
			name: "启用 code-review",
		});
		await waitFor(() => expect(sw).toBeEnabled());
		expect(sw).toBeChecked();
		fireEvent.click(sw);
		await waitFor(() => expect(lastDisabled()).toEqual(["skill:code-review"]));
		const body = home.files["config.yml"] ?? "";
		expect(body).toContain("# 我的配置");
		expect(body).toContain("theme: dark # 深色");
		await waitFor(() => expect(sw).not.toBeChecked());
		fireEvent.click(sw);
		await waitFor(() => expect(lastDisabled()).toBeUndefined());
		expect(home.puts).toHaveLength(2);
	});

	it("config.yml 在别处被改过：不写入，提示冲突并载入最新内容", async () => {
		mountSkills();
		const sw = await screen.findByRole("checkbox", {
			name: "启用 code-review",
		});
		await waitFor(() => expect(sw).toBeEnabled());
		home.files["config.yml"] = "disabledExtensions: [skill:draft]\n";
		fireEvent.click(sw);
		expect(
			await screen.findByText("文件已在别处修改，已载入最新内容，请重新操作。"),
		).toBeInTheDocument();
		expect(home.puts).toEqual([]);
		await waitFor(() =>
			expect(
				screen.getByRole("checkbox", { name: "启用 draft" }),
			).not.toBeChecked(),
		);
	});

	it("新建：校验名称与必填描述，写出带 name / description 的 SKILL.md", async () => {
		mountSkills();
		await screen.findByText("code-review");
		fireEvent.click(screen.getByRole("button", { name: "新建" }));
		const dialog = await screen.findByRole("dialog");
		const name = within(dialog).getByLabelText(/名称/);
		fireEvent.change(name, { target: { value: "Bad Name" } });
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		expect(
			within(dialog).getByText("名称格式不符合要求。"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("必填。")).toBeInTheDocument();
		fireEvent.change(name, { target: { value: "review" } });
		expect(within(dialog).getByText("已有同名条目。")).toBeInTheDocument();
		fireEvent.change(name, { target: { value: "deploy" } });
		fireEvent.change(within(dialog).getByLabelText(/^描述/), {
			target: { value: "发布到生产" },
		});
		fireEvent.change(
			await within(dialog).findByLabelText("editor:skills/deploy/SKILL.md"),
			{ target: { value: "按清单逐项执行。\n" } },
		);
		fireEvent.click(within(dialog).getByRole("button", { name: "创建" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(home.puts).toEqual([
			{
				path: "skills/deploy/SKILL.md",
				body: "---\nname: deploy\ndescription: 发布到生产\n---\n\n按清单逐项执行。\n",
			},
		]);
		expect(await screen.findByText("deploy")).toBeInTheDocument();
	});

	it("编辑：名称只读，未列出的 frontmatter 键原样保留", async () => {
		mountSkills();
		fireEvent.click(
			await screen.findByRole("button", { name: "编辑 code-review" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByLabelText(/名称/)).toBeDisabled();
		fireEvent.change(within(dialog).getByLabelText("描述"), {
			target: { value: "审查暂存区" },
		});
		fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(home.puts[0]).toEqual({
			path: "skills/review/SKILL.md",
			body: "---\nname: code-review\ndescription: 审查暂存区\nhide: true\n---\n\n先读 diff。\n",
		});
	});

	it("编辑期间文件被外部修改：首次保存冲突，再点保存仍不覆盖；载入最新内容后才能保存", async () => {
		mountSkills();
		fireEvent.click(
			await screen.findByRole("button", { name: "编辑 code-review" }),
		);
		const dialog = await screen.findByRole("dialog");
		fireEvent.change(within(dialog).getByLabelText("描述"), {
			target: { value: "我的旧稿" },
		});
		const external =
			"---\nname: code-review\ndescription: 别处改的\n---\n新正文。\n";
		home.files["skills/review/SKILL.md"] = external;

		const save = () =>
			fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
		save();
		expect(
			await within(dialog).findByText(/打开后文件已在别处修改/),
		).toBeInTheDocument();
		// 冲突后查询已刷新到外部内容；草稿仍在，基线仍是打开时的原文
		expect(within(dialog).getByLabelText("描述")).toHaveValue("我的旧稿");
		save();
		await waitFor(() =>
			expect(
				within(dialog).getByRole("button", { name: "保存" }),
			).toBeEnabled(),
		);
		expect(home.puts).toHaveLength(0);
		expect(home.files["skills/review/SKILL.md"]).toBe(external);

		fireEvent.click(
			within(dialog).getByRole("button", {
				name: "载入最新内容（放弃我的改动）",
			}),
		);
		const reloaded = await screen.findByRole("dialog");
		await waitFor(() =>
			expect(within(reloaded).getByLabelText("描述")).toHaveValue("别处改的"),
		);
		fireEvent.change(within(reloaded).getByLabelText("描述"), {
			target: { value: "合并后" },
		});
		fireEvent.click(within(reloaded).getByRole("button", { name: "保存" }));
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(home.puts[0]?.body).toBe(
			"---\nname: code-review\ndescription: 合并后\n---\n\n新正文。\n",
		);
	});

	it("删除：确认后删掉目录下全部文件，并把停用键从 config.yml 清掉", async () => {
		home.files["config.yml"] =
			`disabledExtensions:\n  - ${skillKey("code-review")}\n  - skill:other\n`;
		mountSkills();
		fireEvent.click(
			await screen.findByRole("button", { name: "删除 code-review" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("将删除整个目录（2 个文件），无法撤销。"),
		).toBeInTheDocument();
		fireEvent.click(within(dialog).getByRole("button", { name: "删除" }));
		await waitFor(() =>
			expect(home.deletes.sort()).toEqual([
				"skills/review/SKILL.md",
				"skills/review/scripts/run.sh",
			]),
		);
		await waitFor(() => expect(lastDisabled()).toEqual(["skill:other"]));
		await waitFor(() => expect(screen.queryByText("code-review")).toBeNull());
	});
});
