/** M11b：runner 的 mock 接口（新建线程的目录冲突与 worktree、文件接口必须带 thread、移除 worktree）与前端规则。 */
import { describe, expect, it } from "vitest";
import {
	runnerExecutor,
	runnerPickError,
	worktreeSlug,
} from "../components/runner/RunnerPicker";
import { contentTabs } from "../lib/orb-features";
import { M11B_IDS, RUNNER_REPO } from "./fixtures";
import { MockServer } from "./server";

async function call(
	s: MockServer,
	method: string,
	path: string,
	body?: unknown,
) {
	const res = await s.handleFetch(
		new Request(`http://x/api/v1/${path}`, {
			method,
			body: body === undefined ? undefined : JSON.stringify(body),
		}),
	);
	if (!res) throw new Error(`mock 未处理 ${method} ${path}`);
	return {
		status: res.status,
		json: res.status === 204 ? null : await res.json(),
	};
}

const runnerExec = (extra: Record<string, unknown> = {}) => ({
	prompt: "",
	executor: {
		kind: "runner",
		runnerId: M11B_IDS.laptop,
		dir: RUNNER_REPO,
		...extra,
	},
});

describe("右面板标签按特性（只对 runner）", () => {
	it("普通 orb 不看特性：kind 缺省、features 只有 fs 时仍有终端", () => {
		expect(contentTabs({ features: ["fs"] })).toEqual([
			"changes",
			"files",
			"terminal",
		]);
		expect(contentTabs({ kind: "orb", features: [] })).toHaveLength(3);
		expect(contentTabs(undefined)).toHaveLength(3);
	});
	it("runner 按特性：改动要 git、文件要 fs、终端要 terminal", () => {
		expect(
			contentTabs({ kind: "runner", features: ["fs", "git", "metrics"] }),
		).toEqual(["changes", "files"]);
		expect(
			contentTabs({ kind: "runner", features: ["fs", "terminal"] }),
		).toEqual(["files", "terminal"]);
		expect(contentTabs({ kind: "runner" })).toEqual([]);
	});
});

describe("执行位置的 runner 选择", () => {
	const pick = {
		runnerId: "prov_R",
		runnerName: "laptop",
		dir: { path: "/home/u/code/oryn", name: "oryn", git: true },
		sub: "",
		mode: "current" as const,
		name: "",
	};
	it("worktree 名缺省取提示词前几个词的 slug；非 ASCII 退到 task", () => {
		expect(worktreeSlug("Fix the login redirect loop now please")).toBe(
			"fix-the-login-redirect",
		);
		expect(worktreeSlug("修一下登录")).toBe("task");
	});
	it("子路径只在当前目录模式生效；worktree 模式用仓库根", () => {
		expect(runnerExecutor({ ...pick, sub: "/web/src/" }, "x")).toEqual({
			kind: "runner",
			runnerId: "prov_R",
			dir: "/home/u/code/oryn/web/src",
		});
		expect(
			runnerExecutor({ ...pick, sub: "web", mode: "worktree" }, "Fix login"),
		).toEqual({
			kind: "runner",
			runnerId: "prov_R",
			dir: "/home/u/code/oryn",
			worktree: { name: "fix-login" },
		});
	});
	it("拦下 .. 子路径与非法 worktree 名", () => {
		expect(runnerPickError({ ...pick, sub: "../etc" })).toBe("subpath");
		expect(runnerPickError({ ...pick, mode: "worktree", name: "-x" })).toBe(
			"worktreeName",
		);
		expect(runnerPickError({ ...pick, mode: "worktree", name: "ok.1" })).toBe(
			null,
		);
	});
});

describe("mock runner 接口", () => {
	it("目录已被占用 → 409 dir_in_use（带线程 id）；shareDir 放行；worktree 取 <repo>-<name>", async () => {
		const s = new MockServer();
		const busy = await call(s, "POST", "threads", runnerExec());
		expect(busy.status).toBe(409);
		expect(busy.json).toMatchObject({
			code: "dir_in_use",
			threadIds: [M11B_IDS.runnerThread],
		});
		const shared = await call(
			s,
			"POST",
			"threads",
			runnerExec({ shareDir: true }),
		);
		expect(shared.status).toBe(201);
		expect(shared.json.thread).toMatchObject({
			orbId: M11B_IDS.laptopOrb,
			cwd: RUNNER_REPO,
		});
		const wt = await call(
			s,
			"POST",
			"threads",
			runnerExec({ worktree: { name: "fix-login" } }),
		);
		// fixtures 里已有 fix-login 的 worktree 线程：重名加 -2
		expect(wt.json.thread).toMatchObject({
			cwd: `${RUNNER_REPO}-fix-login-2`,
			worktree: { repo: RUNNER_REPO, branch: "fix-login-2" },
		});
		const outside = await call(
			s,
			"POST",
			"threads",
			runnerExec({ dir: "/etc" }),
		);
		expect(outside.json.code).toBe("dir_not_served");
		const offline = await call(s, "POST", "threads", {
			prompt: "",
			executor: { kind: "runner", runnerId: M11B_IDS.nas, dir: "/srv" },
		});
		expect(offline.json.code).toBe("runner_offline");
	});

	it("runner 的文件接口必须带 thread，范围是线程的工作目录", async () => {
		const s = new MockServer();
		const base = `orbs/${M11B_IDS.laptopOrb}/fs/roots`;
		expect((await call(s, "GET", base)).json.code).toBe("thread_required");
		expect(
			(await call(s, "GET", `${base}?thread=${M11B_IDS.runnerThread}`)).json,
		).toEqual({ home: RUNNER_REPO, workdir: RUNNER_REPO });
	});

	it("移除 worktree 清空线程的 worktree；没有 worktree → 409", async () => {
		const s = new MockServer();
		const path = `threads/${M11B_IDS.worktreeThread}/worktree/remove`;
		expect((await call(s, "POST", path)).status).toBe(204);
		expect(s.threads.get(M11B_IDS.worktreeThread)?.worktree).toBeUndefined();
		expect((await call(s, "POST", path)).status).toBe(409);
	});
});
