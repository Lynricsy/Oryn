/** 项目的 GitHub 触发规则（M14）：列表、对话框的标签输入框 / 预设 / Agent 未连接时禁用保存 / POST 内容。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/client";
import type { GitHubStatus, GitHubTrigger } from "../../api/github-queries";
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
	installations: [],
	automationIdentity: "user",
	webhookUrl: "https://oryn.example/api/v1/public/github/webhook",
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

	it("保存：POST body 含 name、event、label、prompt、announce", async () => {
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
