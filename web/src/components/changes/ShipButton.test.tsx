/** 改动面板的「推送并开 PR」/「推送更新」（M14）与托管项目的「提交并推送到 <基线>」（M24）：点击发固定提示词。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Schemas, Thread } from "../../api/client";
import { qk } from "../../api/queries";
import i18n from "../../i18n";
import { buildHostedShipPrompt, buildShipPrompt } from "../../lib/ship-prompt";
import { makeOrb } from "../../mock/fixtures";
import { realtime } from "../../realtime/socket";
import { getThreadStore, indexStore } from "../../state/stores";
import { ShipButton } from "./ShipButton";

const PROJECT = "prj_01JCZ8M3W4X5Y6Z7A8B9C0D1S1";

function project(bound: boolean): Schemas["Project"] {
	return {
		id: PROJECT,
		name: "webapp",
		repoUrl: "https://github.com/oryn-demo/webapp",
		shallowClone: false,
		createdAt: 1,
		updatedAt: 1,
		snapshots: false,
		warmOrbs: 0,
		github: {
			repoId: 1001,
			fullName: "oryn-demo/webapp",
			bound,
			listen: { reviews: true, comments: true, ci: true },
		},
	} as Schemas["Project"];
}

function thread(id: string, pulls?: Schemas["ThreadPull"][]): Thread {
	return {
		id,
		title: "t",
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		projectId: PROJECT,
		...(pulls ? { pulls } : {}),
	} as Thread;
}

const OPEN_PULL: Schemas["ThreadPull"] = {
	repo: "oryn-demo/webapp",
	number: 128,
	title: "x",
	state: "open",
	draft: false,
	url: "https://github.com/oryn-demo/webapp/pull/128",
	listening: true,
};

function mount(
	threadId: string,
	bound: boolean,
	patch: Partial<Schemas["Project"]> = {},
) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	qc.setQueryData(qk.projects, {
		projects: [{ ...project(bound), ...patch }],
	});
	return render(
		<QueryClientProvider client={qc}>
			<ShipButton threadId={threadId} base="main" />
		</QueryClientProvider>,
	);
}

/** 托管项目（M24）：没有 repoUrl 与 github，基线 trunk。 */
const HOSTED: Partial<Schemas["Project"]> = {
	repoUrl: undefined,
	github: undefined,
	hosted: true,
	baseBranch: "trunk",
};

beforeEach(() => {
	i18n.changeLanguage("zh-CN");
});

afterEach(() => {
	vi.restoreAllMocks();
});

describe("buildShipPrompt", () => {
	it("开 PR：含 gh pr create 与基线分支名", () => {
		const p = buildShipPrompt({ base: "main", update: false });
		expect(p.startsWith("Ship the current work as a pull request:\n1. ")).toBe(
			true,
		);
		expect(p).toContain("gh pr create");
		expect(p).toContain("If HEAD is on `main`");
		expect(p).toContain("against `main`");
		expect(buildShipPrompt({ update: false })).toContain("the base branch");
	});
	it("推送更新", () => {
		expect(buildShipPrompt({ base: "main", update: true })).toBe(
			"Update the open pull request for this work: commit any uncommitted changes that belong to this task, push the branch to origin, and reply with what was pushed.",
		);
	});
	it("托管项目：rebase 后先跑检查再推上基线、实质冲突问人、不强推", () => {
		const p = buildHostedShipPrompt("main");
		expect(p.startsWith("Ship the current work to `main`:\n1. ")).toBe(true);
		const at = (s: string) => {
			const i = p.indexOf(s);
			expect(i, s).toBeGreaterThanOrEqual(0);
			return i;
		};
		// 顺序就是契约：rebase → 检查 → 推送
		expect(at("onto `origin/main`")).toBeLessThan(at("full test suite"));
		expect(at("full test suite")).toBeLessThan(at("git push origin HEAD:main"));
		expect(p).toContain("ask me how to combine them");
		expect(p).toContain("git fetch --unshallow origin");
		expect(p).toContain("do not force-push");
	});
});

describe("ShipButton", () => {
	it("未绑定项目不渲染", () => {
		indexStore.getState().upsertThread(thread("T-ship-unbound"));
		mount("T-ship-unbound", false);
		expect(screen.queryByTestId("ship-button")).toBeNull();
	});

	it("没有 PR：文案「推送并开 PR」，发出的提示词含 gh pr create 与基线分支", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue({} as never);
		indexStore.getState().upsertThread(thread("T-ship-new"));
		mount("T-ship-new", true);
		const btn = screen.getByTestId("ship-button");
		expect(btn).toHaveTextContent("推送并开 PR");
		await act(async () => {
			fireEvent.click(btn);
		});
		const frame = cmd.mock.calls[0]?.[1] as { type: string; message: string };
		expect(frame.type).toBe("prompt");
		expect(frame.message).toContain("gh pr create");
		expect(frame.message).toContain("`main`");
	});

	it("有打开的 PR：文案「推送更新」，提示词以 Update the open pull request 开头", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue({} as never);
		indexStore.getState().upsertThread(thread("T-ship-upd", [OPEN_PULL]));
		mount("T-ship-upd", true);
		const btn = screen.getByTestId("ship-button");
		expect(btn).toHaveTextContent("推送更新");
		await act(async () => {
			fireEvent.click(btn);
		});
		const frame = cmd.mock.calls[0]?.[1] as { message: string };
		expect(frame.message.startsWith("Update the open pull request")).toBe(true);
	});

	it("托管项目：文案「提交并推送到 trunk」，提示词推到 HEAD:trunk", async () => {
		const cmd = vi.spyOn(realtime, "cmd").mockResolvedValue({} as never);
		indexStore.getState().upsertThread(thread("T-ship-hosted", [OPEN_PULL]));
		mount("T-ship-hosted", false, HOSTED);
		const btn = screen.getByTestId("ship-button");
		expect(btn).toHaveTextContent("提交并推送到 trunk");
		await act(async () => {
			fireEvent.click(btn);
		});
		const frame = cmd.mock.calls[0]?.[1] as { message: string };
		expect(frame.message).toContain("HEAD:trunk");
	});

	it("runner 线程不渲染（托管项目也一样）", () => {
		const t = thread("T-ship-runner");
		indexStore.getState().upsertThread(t);
		getThreadStore(t.id)
			.getState()
			.apply([
				{ type: "thread", thread: { ...t, orbId: "orb-ship-runner" } },
				{
					type: "orb",
					orb: {
						...makeOrb("orb-ship-runner", [t.id]),
						kind: "runner",
					} as Orb,
				},
			]);
		mount("T-ship-runner", true, HOSTED);
		expect(screen.queryByTestId("ship-button")).toBeNull();
	});
});
