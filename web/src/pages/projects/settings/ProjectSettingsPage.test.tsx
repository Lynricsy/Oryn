/**
 * 项目设置（M24）：宽屏左导航与重定向、窄屏分组列表；各分组的表单与卡片——概览（基本信息、归档、删除）、
 * 环境（默认环境与脚本、最近一次 setup、快照与预热）、Agent（项目级配置、项目记忆）、GitHub、定时任务。
 * fetch 桩与路由见 projects-test-kit。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../../api/client";
import i18n from "../../../i18n";
import { answerConfirm } from "../../../test/confirm";
import {
	CORE,
	kit,
	mount,
	ORYN,
	orb,
	resetKit,
	SETUP,
	where,
	writesOnly,
} from "../projects-test-kit";

// 编辑器换成 textarea：这里测 Agent 配置卡片的接线，CodeMirror 本身另有测试
vi.mock("../../../components/editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange: (v: string) => void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange(e.target.value)}
		/>
	),
}));

const OVERVIEW = `/projects/${ORYN.id}/settings/overview`;
const ENV = `/projects/${ORYN.id}/settings/environment`;

let width: number;
beforeEach(() => {
	resetKit();
	width = window.innerWidth;
	window.innerWidth = 1280;
});

afterEach(() => {
	window.innerWidth = width;
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("项目设置：导航", () => {
	it("宽屏打开 /settings 落到概览，左导航 6 组", async () => {
		mount(`/projects/${ORYN.id}/settings`);
		await waitFor(() => expect(where()).toHaveTextContent(OVERVIEW));
		const nav = await screen.findByTestId("project-settings-nav");
		expect(within(nav).getAllByRole("link")).toHaveLength(6);
		expect(within(nav).getByRole("link", { name: "概览" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(
			screen.getByRole("heading", { level: 1, name: "概览" }),
		).toBeInTheDocument();
	});

	it("不认识的分组落到概览", async () => {
		mount(`/projects/${ORYN.id}/settings/bogus`);
		await waitFor(() => expect(where()).toHaveTextContent(OVERVIEW));
	});

	it("窄屏：/settings 是分组列表，点「环境」进入该组", async () => {
		window.innerWidth = 390;
		mount(`/projects/${ORYN.id}/settings`);
		const list = await screen.findByTestId("project-settings-list");
		expect(within(list).getAllByRole("link")).toHaveLength(6);
		expect(screen.queryByTestId("project-settings-nav")).toBeNull();
		fireEvent.click(within(list).getByRole("link", { name: "环境" }));
		await waitFor(() => expect(where()).toHaveTextContent(ENV));
		expect(await screen.findByLabelText(/^setup 脚本/)).toBeInTheDocument();
		expect(screen.queryByTestId("project-settings-list")).toBeNull();
	});
});

describe("项目设置：概览", () => {
	it("编辑后保存：只发概览字段（空串 = 清除），面包屑跟着改名", async () => {
		mount(OVERVIEW);
		const name = await screen.findByLabelText("名称");
		fireEvent.change(name, { target: { value: "oryn2" } });
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(writesOnly()).toEqual([
			[
				"PATCH",
				`/api/v1/projects/${ORYN.id}`,
				{
					name: "oryn2",
					description: "",
					repoUrl: "https://github.com/x/oryn",
					baseBranch: "",
					icon: "\u{f07d3}",
					gitAuthorName: "",
					gitAuthorEmail: "",
				},
			],
		]);
		expect(where()).toHaveTextContent(OVERVIEW);
		const crumbs = screen.getByRole("navigation", { name: "位置" });
		await waitFor(() =>
			expect(
				within(crumbs).getByRole("link", { name: "oryn2" }),
			).toHaveAttribute("href", `/projects/${ORYN.id}`),
		);
	});

	it("编辑时取消：表单还原为服务端值，不发请求", async () => {
		mount(OVERVIEW);
		const name = await screen.findByLabelText("名称");
		fireEvent.change(name, { target: { value: "scratch" } });
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		expect(screen.getByLabelText("名称")).toHaveValue("oryn");
		expect(kit.writes).toEqual([]);
	});

	it("选字形后保存发送该字形；选「自动」发送空串清除", async () => {
		mount(OVERVIEW);
		const grid = await screen.findByRole("group", {
			name: i18n.t("projects.icon"),
		});
		expect(
			within(grid).getByRole("button", { name: "language go" }),
		).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(
			within(grid).getByRole("button", { name: "language rust" }),
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		fireEvent.click(
			await screen.findByRole("button", { name: i18n.t("projects.iconAuto") }),
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(2));
		expect(kit.writes.map((w) => w.body)).toEqual([
			expect.objectContaining({ icon: "\u{f1617}" }),
			expect.objectContaining({ icon: "" }),
		]);
	});

	it("归档：确认框写明后果，确认后 PATCH archived=true 并出现归档提示", async () => {
		mount(OVERVIEW);
		fireEvent.click(await screen.findByRole("button", { name: "归档" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("归档项目 oryn？");
		expect(req.body).toBe(i18n.t("projectSettings.archiveConfirmBody"));
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				["PATCH", `/api/v1/projects/${ORYN.id}`, { archived: true }],
			]),
		);
		expect(await screen.findByTestId("project-archived")).toBeInTheDocument();
		// 危险区换成「取消归档」，点了直接生效（可逆，不确认）
		fireEvent.click(
			within(screen.getByTestId("project-danger")).getByRole("button", {
				name: "取消归档",
			}),
		);
		await waitFor(() => expect(kit.writes).toHaveLength(2));
		expect(kit.writes[1]?.body).toEqual({ archived: false });
	});

	it("归档时取消确认：不发请求", async () => {
		mount(OVERVIEW);
		fireEvent.click(await screen.findByRole("button", { name: "归档" }));
		await answerConfirm(false);
		expect(kit.writes).toEqual([]);
	});

	it("删除：确认后 DELETE 并回项目总览", async () => {
		mount(OVERVIEW);
		fireEvent.click(await screen.findByRole("button", { name: "删除" }));
		const req = await answerConfirm(true);
		expect(req.title).toBe("删除项目 oryn？");
		expect(req.body).toBe(i18n.t("projectSettings.deleteHint"));
		await waitFor(() => expect(where()).toHaveTextContent(/^\/projects$/));
		expect(writesOnly()).toEqual([
			["DELETE", `/api/v1/projects/${ORYN.id}`, undefined],
		]);
	});

	it("删除时取消确认：不发请求", async () => {
		mount(OVERVIEW);
		fireEvent.click(await screen.findByRole("button", { name: "删除" }));
		await answerConfirm(false);
		expect(kit.writes).toEqual([]);
		expect(where()).toHaveTextContent(OVERVIEW);
	});
});

const SNAP = (
	over: Partial<Schemas["ProjectSnapshot"]> &
		Pick<Schemas["ProjectSnapshot"], "id" | "state">,
): Schemas["ProjectSnapshot"] => ({
	providerId: "prov_1",
	providerName: "local",
	type: "medium",
	configHash: "h",
	depth: 0,
	lineageAt: Date.now() - 2 * 3_600_000,
	createdAt: 1,
	useCount: 0,
	...over,
});

describe("项目设置：环境", () => {
	it("保存只发环境字段", async () => {
		mount(ENV);
		fireEvent.change(await screen.findByLabelText(/^setup 脚本/), {
			target: { value: "make deps" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(writesOnly()).toEqual([
			[
				"PATCH",
				`/api/v1/projects/${ORYN.id}`,
				{
					defaultProvider: "",
					defaultOrbType: "",
					defaultBrowserProfileId: "",
					setupScript: "make deps",
					resumeScript: "",
					shallowClone: false,
					snapshots: true,
					warmOrbs: 0,
				},
			],
		]);
	});

	it("默认浏览器 Profile：选中后保存带它的 id", async () => {
		mount(ENV);
		await screen.findByRole("option", { name: "工作账号" });
		fireEvent.change(
			screen.getByRole("combobox", {
				name: new RegExp(`^${i18n.t("projects.defaultBrowserProfile")}`),
			}),
			{ target: { value: "bpf_A" } },
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(kit.writes[0]?.body).toEqual(
			expect.objectContaining({ defaultBrowserProfileId: "bpf_A" }),
		);
	});

	it("没有 setup 记录：环境配置卡是空状态", async () => {
		mount(ENV);
		expect(await screen.findByTestId("project-env-empty")).toHaveTextContent(
			i18n.t("projectsPage.noSetupRun"),
		);
	});

	it("最近一次 setup：结果、触发方式、仓库内脚本与打开线程；别的项目的不算", async () => {
		kit.orbs = [
			orb({
				id: "orb-old",
				threadIds: ["T-9"],
				lastSetup: { ...SETUP, id: "run_0", finishedAt: 5, ok: false },
			}),
			orb({ id: "orb-1", threadIds: ["T-1", "T-2"], lastSetup: SETUP }),
			// 别的项目的 orb 不算（它的 setup 更晚、还失败了）
			orb({
				id: "orb-x",
				projectId: CORE.id,
				threadIds: ["T-x"],
				lastSetup: { ...SETUP, id: "run_x", finishedAt: 99_000, ok: false },
			}),
		];
		mount(ENV);
		const env = await screen.findByTestId("project-env");
		expect(env).toHaveTextContent("成功");
		expect(env).toHaveTextContent("自动");
		expect(env).toHaveTextContent(
			i18n.t("env.repoScript", { value: i18n.t("env.repoScriptYes") }),
		);
		expect(within(env).getByRole("link", { name: "打开线程" })).toHaveAttribute(
			"href",
			"/t/T-1",
		);
	});

	it("快照开关：关掉后保存，PATCH 带 snapshots=false", async () => {
		mount(ENV);
		const toggle = await screen.findByRole("checkbox", {
			name: i18n.t("accel.snapshotsToggle"),
		});
		expect(toggle).toBeChecked();
		fireEvent.click(toggle);
		expect(toggle).not.toBeChecked();
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(kit.writes[0]?.body).toEqual(
			expect.objectContaining({ snapshots: false, warmOrbs: 0 }),
		);
	});

	it("预热步进器：0–3，到上限后「增加」禁用，保存带 warmOrbs", async () => {
		kit.projects = [{ ...ORYN, warmOrbs: 2 }, CORE];
		mount(ENV);
		const value = await screen.findByTestId("warm-orbs-value");
		const inc = screen.getByRole("button", { name: i18n.t("accel.warmInc") });
		const dec = screen.getByRole("button", { name: i18n.t("accel.warmDec") });
		expect(value).toHaveTextContent("2");
		for (let i = 0; i < 3; i++) fireEvent.click(dec);
		expect(value).toHaveTextContent("0");
		expect(dec).toBeDisabled();
		for (let i = 0; i < 3; i++) fireEvent.click(inc);
		expect(value).toHaveTextContent("3");
		expect(inc).toBeDisabled();
		fireEvent.click(inc);
		expect(value).toHaveTextContent("3");
		// 说明文字写明全局上限与常驻占用内存
		const hint = screen.getByText(i18n.t("accel.warmHint"));
		expect(hint).toHaveTextContent("pool.max_total");
		expect(hint).toHaveTextContent("常驻占用内存");
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(kit.writes[0]?.body).toEqual(
			expect.objectContaining({ warmOrbs: 3 }),
		);
	});

	it("快照表：三种状态、提交前 7 位、使用次数与退役原因", async () => {
		kit.snapshots = [
			SNAP({ id: "s-b", state: "building", type: "large" }),
			SNAP({
				id: "s-r",
				state: "ready",
				baseCommit: "abc1234def5678",
				useCount: 7,
				sizeBytes: 1_240_000_000,
			}),
			SNAP({
				id: "s-x",
				state: "retired",
				type: "small",
				retireReason: "superseded",
			}),
		];
		mount(ENV);
		const table = await screen.findByTestId("snapshot-list");
		const row = (id: string) =>
			table.querySelector(`[data-snapshot="${id}"]`) as HTMLElement;
		expect(row("s-b")).toHaveTextContent("建立中");
		expect(row("s-r")).toHaveTextContent("可用");
		expect(row("s-r")).toHaveTextContent("abc1234");
		expect(row("s-r")).not.toHaveTextContent("abc1234d");
		expect(row("s-r")).toHaveTextContent("1.2 GB");
		expect(row("s-r")).toHaveTextContent("7");
		expect(row("s-r")).toHaveTextContent("2h 0m");
		expect(row("s-x")).toHaveTextContent("已退役");
		expect(row("s-x")).toHaveTextContent("已被新快照取代");
	});

	it("清除快照：取消确认不发请求；确认后 DELETE 并刷新列表", async () => {
		kit.snapshots = [
			SNAP({ id: "s-r", state: "ready", baseCommit: "abc1234def" }),
		];
		mount(ENV);
		const clear = await screen.findByRole("button", {
			name: i18n.t("accel.clear"),
		});
		fireEvent.click(clear);
		expect((await answerConfirm(false)).title).toBe(
			i18n.t("accel.clearConfirm", { name: ORYN.name }),
		);
		expect(kit.writes).toEqual([]);
		fireEvent.click(clear);
		await answerConfirm(true);
		await waitFor(() => expect(kit.writes).toHaveLength(1));
		expect(writesOnly()).toEqual([
			["DELETE", `/api/v1/projects/${ORYN.id}/snapshots`, undefined],
		]);
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: i18n.t("accel.clear") }),
			).toBeNull(),
		);
		expect(screen.getByTestId("snapshot-list")).toHaveTextContent("手动清除");
	});

	it("预热 orb 列表：池 orb 带「预热」徽标与 Docker 徽标；非池 orb 不列", async () => {
		kit.orbs = [
			orb({
				id: "orb-pool",
				threadIds: [],
				pool: "idle",
				state: "frozen",
				nestedDocker: true,
			}),
			orb({ id: "orb-work" }),
		];
		mount(ENV);
		const card = await screen.findByTestId("project-accel");
		await waitFor(() =>
			expect(card.querySelector('[data-pool-orb="orb-pool"]')).not.toBeNull(),
		);
		const row = card.querySelector('[data-pool-orb="orb-pool"]') as HTMLElement;
		expect(row).toHaveTextContent(i18n.t("accel.warm"));
		expect(row).toHaveTextContent("Docker");
		expect(row).toHaveTextContent(i18n.t("accel.pool.idle"));
		expect(card.querySelector('[data-pool-orb="orb-work"]')).toBeNull();
	});

	it("没有预热 orb：开了预热时说明暂不满足条件", async () => {
		kit.projects = [{ ...ORYN, warmOrbs: 1 }, CORE];
		mount(ENV);
		expect(await screen.findByTestId("pool-empty")).toHaveTextContent(
			i18n.t("accel.pool.pending"),
		);
		expect(await screen.findByTestId("snapshots-empty")).toBeInTheDocument();
	});
});

describe("项目设置：Agent", () => {
	const agentCard = async () =>
		(
			await screen.findByRole("heading", {
				name: i18n.t("projectsPage.agentConfig.title"),
			})
		).closest("section") as HTMLElement;

	it("列出该项目的配置；编辑保存走项目接口，写入警告逐条显示在卡片下", async () => {
		kit.agentFiles = {
			[ORYN.id]: { "mcp.json": "{}\n", "rules/a.md": "a\n" },
			[CORE.id]: { "AGENTS.md": "core\n" },
		};
		mount(`/projects/${ORYN.id}/settings/agent`);
		const card = await agentCard();
		expect(card).toHaveTextContent(i18n.t("projectsPage.agentConfig.hint"));
		expect(card).toHaveTextContent(
			i18n.t("projectsPage.agentConfig.secretHint"),
		);
		expect(card).toHaveTextContent(i18n.t("projectsPage.agentConfig.pushHint"));
		const scope = within(card);
		expect(
			await scope.findByRole("tab", {
				name: i18n.t("files.roots.projectAgentConfig"),
			}),
		).toBeInTheDocument();
		expect(await scope.findByText("rules")).toBeInTheDocument();
		expect(scope.queryByText("AGENTS.md")).toBeNull();
		fireEvent.click(scope.getByText("mcp.json"));
		const editor = await scope.findByLabelText("editor:mcp.json");
		expect(editor).toHaveValue("{}\n");
		fireEvent.change(editor, {
			target: { value: '{\n  "token": "Bearer ghp_x"\n}\n' },
		});
		fireEvent.click(scope.getByRole("button", { name: "保存" }));
		expect(
			await scope.findByText("mcp.json:2 — looks like a key"),
		).toBeInTheDocument();
		expect(writesOnly()).toEqual([
			[
				"PUT",
				`/api/v1/projects/${ORYN.id}/agent-config/files/mcp.json`,
				'{\n  "token": "Bearer ghp_x"\n}\n',
			],
		]);
	});

	it("项目记忆开关：PUT 本项目的开关", async () => {
		kit.memoryProjects = [
			{
				projectId: ORYN.id,
				name: ORYN.name,
				enabled: false,
				overridden: false,
			},
			{ projectId: CORE.id, name: CORE.name, enabled: true, overridden: false },
		];
		mount(`/projects/${ORYN.id}/settings/agent`);
		const toggle = await screen.findByRole("checkbox", {
			name: i18n.t("memoryPage.projects.toggle", { name: ORYN.name }),
		});
		expect(
			screen.queryByRole("checkbox", {
				name: i18n.t("memoryPage.projects.toggle", { name: CORE.name }),
			}),
		).toBeNull();
		fireEvent.click(toggle);
		await waitFor(() =>
			expect(writesOnly()).toEqual([
				["PUT", `/api/v1/memory/projects/${ORYN.id}`, { enabled: true }],
			]),
		);
	});

	it("记忆列表里没有本项目（已归档）：只给说明", async () => {
		mount(`/projects/${ORYN.id}/settings/agent`);
		expect(
			await screen.findByText(i18n.t("projectSettings.memory.archived")),
		).toBeInTheDocument();
	});
});

describe("项目设置：GitHub 与定时任务", () => {
	it("仓库不在 GitHub 上：空态与去概览改地址的入口", async () => {
		mount(`/projects/${CORE.id}/settings/github`);
		expect(
			await screen.findByText(i18n.t("projectSettings.github.noneTitle")),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", {
				name: i18n.t("projectSettings.github.editRepo"),
			}),
		).toHaveAttribute("href", `/projects/${CORE.id}/settings/overview`);
		expect(screen.queryByTestId("project-gates")).toBeNull();
	});

	it("定时任务分组：渲染该项目的定时列表", async () => {
		mount(`/projects/${ORYN.id}/settings/schedules`);
		const box = await screen.findByTestId(`project-schedules-${ORYN.id}`);
		expect(
			await within(box).findByText(i18n.t("projects.noSchedules")),
		).toBeInTheDocument();
	});
});
