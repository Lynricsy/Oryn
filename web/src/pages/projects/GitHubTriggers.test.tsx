/**
 * 项目的 GitHub 触发规则（M14 / M15）：列表、对话框的标签输入框 / 预设 / Agent 未连接时禁用保存 / POST 内容；
 * M15 的新事件与按事件出现的字段、驱动者与维护者、思考级别、时限换算、预设按钮（含应用预设门禁）、编辑时的模型池。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/client";
import type {
	GitHubPreset,
	GitHubStatus,
	GitHubTrigger,
} from "../../api/github-queries";
import i18n from "../../i18n";
import { TRIGGER_PRESETS } from "../../lib/trigger-presets";
import { GitHubTriggers } from "./GitHubTriggers";

const T = (k: string) => i18n.t(k as "common.copy");

const PROJECT: Project = {
	id: "prj_1",
	name: "widgets",
	repoUrl: "https://github.com/acme/widgets",
	shallowClone: false,
	snapshots: true,
	warmOrbs: 0,
	createdAt: 1,
	updatedAt: 1,
	github: {
		repoId: 42,
		fullName: "acme/widgets",
		bound: true,
		listen: { reviews: true, comments: true, ci: true },
	},
};

const STATUS: GitHubStatus = {
	identities: {},
	automationIdentity: "user",
	callbackUrl: "https://oryn.example/api/v1/public/github/oauth/callback",
	webhookBase: "https://oryn.example",
	webUrl: "https://github.com",
};

const TRIGGER: GitHubTrigger = {
	id: "ght_01JDM000000000000000000001",
	projectId: PROJECT.id,
	name: "分诊",
	event: "issue_labeled",
	label: "oryn",
	prompt: "Triage it",
	enabled: true,
	announce: true,
	runCount: 4,
	lastRunAt: Date.now() - 60_000,
	createdAt: 1,
	updatedAt: 1,
};

const PRESETS: GitHubPreset[] = [
	{
		id: "robomp_triage",
		name: "robomp 分诊",
		trigger: {
			name: "robomp 分诊",
			event: "issue_opened",
			prompt: "Triage {{repo}}#{{number}}",
			drivers: "writers_and_author",
			implAuth: true,
			maxDurationSec: 7200,
			thinking: "high",
		},
		gates: { push: true, pr: true, checkCmd: "make check" },
	},
	{
		id: "robomp_release",
		name: "robomp 发布哨兵",
		trigger: {
			name: "robomp 发布哨兵",
			event: "workflow_run_completed",
			prompt: "Fix the release",
			releasePrefix: "chore: release ",
		},
	},
];

/** 新建规则不改任何 M15 字段时的请求体默认部分。 */
const DEFAULTS = {
	announce: true,
	drivers: "writers_and_author",
	maintainers: [],
	reviewerBots: [],
	models: [],
	thinking: "",
	implAuth: false,
	commentOnlyReviews: false,
	autocloseHours: 4,
	maxDurationSec: 0,
	completionReminders: false,
};

/** 无障碍名以某个文案开头（Field 的名字里还带着说明）。 */
const starts = (k: string) => (name: string) => name.startsWith(T(k));

let sent: { method: string; path: string; body: unknown }[];

function renderList(triggers: GitHubTrigger[], status = STATUS) {
	sent = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: Request) => {
			const u = new URL(input.url);
			if (input.method !== "GET") {
				const body = input.method === "DELETE" ? null : await input.json();
				sent.push({ method: input.method, path: u.pathname, body });
				return input.method === "DELETE"
					? new Response(null, { status: 204 })
					: Response.json({ ...TRIGGER, ...(body as object) }, { status: 201 });
			}
			if (u.pathname === "/api/v1/github") return Response.json(status);
			if (u.pathname === "/api/v1/github/presets")
				return Response.json({ presets: PRESETS });
			if (u.pathname === `/api/v1/projects/${PROJECT.id}/github/triggers`)
				return Response.json({ triggers });
			return Response.json({});
		}),
	);
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<GitHubTriggers project={PROJECT} />
		</QueryClientProvider>,
	);
}

async function openNew() {
	fireEvent.click(
		await screen.findByRole("button", { name: T("github.triggers.new") }),
	);
	return screen.findByTestId("github-trigger-dialog");
}

const eventSelect = () =>
	screen.getByRole("combobox", { name: T("github.triggers.eventLabel") });
const saveButton = () => screen.getByRole("button", { name: T("common.save") });

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("GitHubTriggers", () => {
	it("列表：名称、事件、次数；没有规则时显示说明", async () => {
		renderList([TRIGGER]);
		const row = await screen.findByTestId("github-trigger-row");
		expect(row).toHaveTextContent("分诊");
		expect(row).toHaveTextContent("oryn");
		expect(row).toHaveTextContent("已触发 4 次");
	});

	it("没有规则：一行说明", async () => {
		renderList([]);
		expect(
			await screen.findByText(T("github.triggers.empty")),
		).toBeInTheDocument();
	});

	it("只有选「打指定标签」才出现标签输入框", async () => {
		renderList([]);
		await openNew();
		expect(
			screen.getByRole("textbox", { name: T("github.triggers.label") }),
		).toBeInTheDocument();
		fireEvent.change(eventSelect(), { target: { value: "issue_opened" } });
		expect(
			screen.queryByRole("textbox", { name: T("github.triggers.label") }),
		).toBeNull();
		fireEvent.change(eventSelect(), { target: { value: "issue_labeled" } });
		expect(
			screen.getByRole("textbox", { name: T("github.triggers.label") }),
		).toBeInTheDocument();
	});

	it("套用「分诊并修复」：提示词被覆盖，以 Triage {{kind}} 开头", async () => {
		renderList([]);
		await openNew();
		const prompt = screen.getByRole("textbox", {
			name: new RegExp(T("github.triggers.prompt")),
		}) as HTMLTextAreaElement;
		fireEvent.change(prompt, { target: { value: "old" } });
		fireEvent.change(
			screen.getByRole("combobox", { name: T("github.triggers.preset") }),
			{ target: { value: "triage" } },
		);
		expect(prompt.value.startsWith("Triage {{kind}}")).toBe(true);
		expect(prompt.value).toBe(TRIGGER_PRESETS[0].prompt);
	});

	it("Agent 未连接时选「@Agent 账号」：显示警告、保存禁用", async () => {
		renderList([]);
		await openNew();
		fireEvent.change(
			screen.getByRole("textbox", { name: T("github.triggers.name") }),
			{ target: { value: "响应 @" } },
		);
		fireEvent.change(
			screen.getByRole("textbox", {
				name: new RegExp(T("github.triggers.prompt")),
			}),
			{ target: { value: "Do it" } },
		);
		fireEvent.change(eventSelect(), { target: { value: "mention" } });
		expect(
			await screen.findByText(T("github.triggers.agentMissing")),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
		fireEvent.change(eventSelect(), { target: { value: "issue_opened" } });
		expect(saveButton()).toBeEnabled();
	});

	it("Agent 已连接：@Agent 账号可保存", async () => {
		renderList([], {
			...STATUS,
			identities: {
				agent: {
					login: "oryn-bot",
					githubId: 7,
					avatarUrl: "",
					status: "ok",
					connectedAt: 1,
				},
			},
		});
		await openNew();
		fireEvent.change(
			screen.getByRole("textbox", { name: T("github.triggers.name") }),
			{ target: { value: "响应 @" } },
		);
		fireEvent.change(
			screen.getByRole("textbox", {
				name: new RegExp(T("github.triggers.prompt")),
			}),
			{ target: { value: "Do it" } },
		);
		fireEvent.change(eventSelect(), { target: { value: "mention" } });
		await waitFor(() => expect(saveButton()).toBeEnabled());
		expect(screen.queryByText(T("github.triggers.agentMissing"))).toBeNull();
	});

	it("保存：POST body 含 name、event、label、prompt、announce 与 M15 默认值", async () => {
		renderList([]);
		await openNew();
		fireEvent.change(
			screen.getByRole("textbox", { name: T("github.triggers.name") }),
			{ target: { value: " 分诊 " } },
		);
		fireEvent.change(
			screen.getByRole("textbox", { name: T("github.triggers.label") }),
			{ target: { value: "oryn" } },
		);
		fireEvent.click(
			screen.getByRole("checkbox", { name: T("github.triggers.announce") }),
		);
		fireEvent.change(
			screen.getByRole("textbox", {
				name: new RegExp(T("github.triggers.prompt")),
			}),
			{ target: { value: "Triage {{repo}}#{{number}}" } },
		);
		fireEvent.click(saveButton());
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]).toEqual({
			method: "POST",
			path: `/api/v1/projects/${PROJECT.id}/github/triggers`,
			body: {
				...DEFAULTS,
				name: "分诊",
				event: "issue_labeled",
				label: "oryn",
				prompt: "Triage {{repo}}#{{number}}",
				announce: false,
			},
		});
		await waitFor(() =>
			expect(screen.queryByTestId("github-trigger-dialog")).toBeNull(),
		);
	});

	it("事件下拉含 M15 新事件；「只发 COMMENT」只在外部 PR、发布前缀只在发布哨兵出现", async () => {
		renderList([]);
		await openNew();
		const values = within(eventSelect())
			.getAllByRole("option")
			.map((o) => (o as HTMLOptionElement).value);
		expect(values).toEqual([
			"issue_opened",
			"issue_reopened",
			"issue_labeled",
			"mention",
			"pull_request_opened",
			"workflow_run_completed",
		]);
		const commentOnly = () =>
			screen.queryByRole("checkbox", {
				name: T("github.triggers.commentOnlyReviews"),
			});
		const prefix = () =>
			screen.queryByRole("textbox", {
				name: starts("github.triggers.releasePrefix"),
			});
		expect(commentOnly()).toBeNull();
		expect(prefix()).toBeNull();
		fireEvent.change(eventSelect(), {
			target: { value: "pull_request_opened" },
		});
		expect(commentOnly()).toBeInTheDocument();
		expect(prefix()).toBeNull();
		fireEvent.change(eventSelect(), {
			target: { value: "workflow_run_completed" },
		});
		expect(commentOnly()).toBeNull();
		expect(prefix()).toHaveValue("chore: bump version to ");
		// 驱动者默认「有写权限的人和作者」
		expect(
			screen.getByRole("radio", {
				name: starts("github.triggers.drivers.writers_and_author"),
			}),
		).toBeChecked();
	});

	it("保存 M15 字段：驱动者、维护者（去 @ 去重）、评审机器人、思考级别、时限分钟换算秒、发布前缀", async () => {
		renderList([]);
		await openNew();
		fireEvent.change(
			screen.getByRole("textbox", { name: T("github.triggers.name") }),
			{ target: { value: "发布" } },
		);
		fireEvent.change(
			screen.getByRole("textbox", {
				name: new RegExp(T("github.triggers.prompt")),
			}),
			{ target: { value: "Fix it" } },
		);
		fireEvent.change(eventSelect(), {
			target: { value: "workflow_run_completed" },
		});
		fireEvent.click(
			screen.getByRole("radio", {
				name: starts("github.triggers.drivers.anyone"),
			}),
		);
		fireEvent.change(
			screen.getByRole("textbox", {
				name: starts("github.triggers.maintainers"),
			}),
			{ target: { value: "@alice, bob alice" } },
		);
		fireEvent.change(
			screen.getByRole("textbox", {
				name: starts("github.triggers.reviewerBots"),
			}),
			{ target: { value: "coderabbitai[bot]" } },
		);
		fireEvent.change(
			screen.getByRole("combobox", { name: T("github.triggers.thinking") }),
			{ target: { value: "xhigh" } },
		);
		fireEvent.click(
			screen.getByRole("checkbox", { name: T("github.triggers.implAuth") }),
		);
		fireEvent.click(
			screen.getByRole("checkbox", {
				name: T("github.triggers.completionReminders"),
			}),
		);
		fireEvent.change(
			screen.getByRole("spinbutton", {
				name: starts("github.triggers.autocloseHours"),
			}),
			{ target: { value: "6" } },
		);
		const duration = screen.getByRole("spinbutton", {
			name: starts("github.triggers.maxDuration"),
		});
		fireEvent.change(duration, { target: { value: "-5" } });
		expect(saveButton()).toBeDisabled();
		fireEvent.change(duration, { target: { value: "90" } });
		fireEvent.change(
			screen.getByRole("textbox", {
				name: starts("github.triggers.releasePrefix"),
			}),
			{ target: { value: "release: v" } },
		);
		fireEvent.click(saveButton());
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]?.body).toEqual({
			...DEFAULTS,
			name: "发布",
			event: "workflow_run_completed",
			prompt: "Fix it",
			drivers: "anyone",
			maintainers: ["alice", "bob"],
			reviewerBots: ["coderabbitai[bot]"],
			thinking: "xhigh",
			implAuth: true,
			completionReminders: true,
			autocloseHours: 6,
			maxDurationSec: 5400,
			releasePrefix: "release: v",
		});
	});

	it("预设：点按钮填满表单；带门禁的预设可应用到本项目（PATCH githubGates）", async () => {
		renderList([]);
		await openNew();
		fireEvent.click(await screen.findByRole("button", { name: "robomp 分诊" }));
		expect(
			screen.getByRole("textbox", { name: T("github.triggers.name") }),
		).toHaveValue("robomp 分诊");
		expect(eventSelect()).toHaveValue("issue_opened");
		expect(
			screen.getByRole("textbox", {
				name: new RegExp(T("github.triggers.prompt")),
			}),
		).toHaveValue("Triage {{repo}}#{{number}}");
		expect(
			screen.getByRole("checkbox", { name: T("github.triggers.implAuth") }),
		).toBeChecked();
		expect(
			screen.getByRole("spinbutton", {
				name: starts("github.triggers.maxDuration"),
			}),
		).toHaveValue(120);
		expect(
			screen.getByRole("combobox", { name: T("github.triggers.thinking") }),
		).toHaveValue("high");
		expect(screen.getByText(T("github.triggers.presetGates"))).toBeVisible();
		fireEvent.click(
			screen.getByRole("button", {
				name: T("github.triggers.presetGatesApply"),
			}),
		);
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]).toEqual({
			method: "PATCH",
			path: `/api/v1/projects/${PROJECT.id}`,
			body: { githubGates: { push: true, pr: true, checkCmd: "make check" } },
		});
		expect(
			await screen.findByText(T("github.triggers.presetGatesApplied")),
		).toBeInTheDocument();
		// 不带门禁的预设：提示消失
		fireEvent.click(screen.getByRole("button", { name: "robomp 发布哨兵" }));
		expect(screen.queryByText(T("github.triggers.presetGates"))).toBeNull();
		expect(
			screen.getByRole("textbox", {
				name: starts("github.triggers.releasePrefix"),
			}),
		).toHaveValue("chore: release ");
	});

	it("编辑：回填 M15 字段；移除模型后 PATCH 带上剩下的模型池", async () => {
		renderList([
			{
				...TRIGGER,
				models: [
					{ provider: "anthropic", id: "claude-sonnet-4-5" },
					{ provider: "openai", id: "gpt-5" },
				],
				maxDurationSec: 1800,
				maintainers: ["alice"],
			},
		]);
		fireEvent.click(
			await screen.findByRole("button", {
				name: T("github.triggers.edit").replace("{{name}}", TRIGGER.name),
			}),
		);
		const dialog = await screen.findByTestId("github-trigger-dialog");
		// 编辑时不显示预设按钮
		expect(within(dialog).queryByTestId("trigger-presets")).toBeNull();
		expect(within(dialog).getAllByTestId("trigger-model")).toHaveLength(2);
		expect(
			screen.getByRole("textbox", {
				name: starts("github.triggers.maintainers"),
			}),
		).toHaveValue("alice");
		fireEvent.click(
			screen.getByRole("button", {
				name: T("github.triggers.removeModel").replace(
					"{{model}}",
					"openai/gpt-5",
				),
			}),
		);
		expect(within(dialog).getAllByTestId("trigger-model")).toHaveLength(1);
		fireEvent.click(saveButton());
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]?.method).toBe("PATCH");
		expect(sent[0]?.body).toMatchObject({
			models: [{ provider: "anthropic", id: "claude-sonnet-4-5" }],
			maxDurationSec: 1800,
			maintainers: ["alice"],
			label: "oryn",
		});
	});

	it("启停开关发 PATCH { enabled }", async () => {
		renderList([TRIGGER]);
		const toggle = await screen.findByRole("checkbox", {
			name: `启用「${TRIGGER.name}」`,
		});
		fireEvent.click(toggle);
		await waitFor(() => expect(sent).toHaveLength(1));
		expect(sent[0]).toEqual({
			method: "PATCH",
			path: `/api/v1/github/triggers/${TRIGGER.id}`,
			body: { enabled: false },
		});
	});
});
