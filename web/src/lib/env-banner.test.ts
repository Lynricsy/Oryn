import { describe, expect, it } from "vitest";
import {
	buildEnvPrompt,
	type EnvBannerInput,
	LOG_TAIL_LINES,
	latestSetupByProject,
	type SetupRun,
	selectEnvBanner,
	tailLines,
} from "./env-banner";

function run(p: Partial<SetupRun> = {}): SetupRun {
	return {
		id: "osr_a",
		orbId: "orb-1",
		phase: "setup",
		trigger: "auto",
		startedAt: 0,
		finishedAt: 1_000,
		ok: true,
		setupRan: true,
		durationMs: 30_000,
		hasRepoScript: true,
		hasProjectScript: false,
		...p,
	};
}

const REPO = { repoUrl: "https://github.com/x/y" };

function input(
	orb: Partial<NonNullable<EnvBannerInput["orb"]>> = {},
	extra: Partial<EnvBannerInput> = {},
): EnvBannerInput {
	return {
		thread: { state: "idle" },
		orb: { state: "ready", ...orb },
		project: REPO,
		...extra,
	};
}

const NONE = new Set<string>();

describe("环境提示条选择", () => {
	it("正常环境不显示提示条", () => {
		expect(selectEnvBanner(input({ lastSetup: run() }), NONE)).toBeNull();
		// 还没跑过 setup：不能断定缺少配置
		expect(selectEnvBanner(input(), NONE)).toBeNull();
	});

	it("setup 失败优先于 resume 失败，关闭后才露出 resume 失败", () => {
		const setup = run({ id: "osr_s", ok: false, error: "exit 1" });
		const resume = run({ id: "osr_r", phase: "resume", ok: false });
		const i = input({ lastSetup: setup, lastResume: resume });
		expect(selectEnvBanner(i, NONE)).toMatchObject({
			kind: "run_failed",
			run: { id: "osr_s" },
		});
		expect(selectEnvBanner(i, new Set(["failed:osr_s"]))).toMatchObject({
			kind: "run_failed",
			run: { id: "osr_r" },
		});
	});

	it("关闭按运行记录区分：同一阶段的新失败会再次出现", () => {
		const dismissed = new Set(["failed:osr_old"]);
		expect(
			selectEnvBanner(
				input({ lastSetup: run({ id: "osr_old", ok: false }) }),
				dismissed,
			),
		).toBeNull();
		expect(
			selectEnvBanner(
				input({ lastSetup: run({ id: "osr_new", ok: false }) }),
				dismissed,
			),
		).toMatchObject({ kind: "run_failed", key: "failed:osr_new" });
	});

	it("setup 实际执行且 ≥2 分钟才提示过慢", () => {
		expect(
			selectEnvBanner(input({ lastSetup: run({ durationMs: 120_000 }) }), NONE),
		).toMatchObject({ kind: "slow_setup" });
		expect(
			selectEnvBanner(input({ lastSetup: run({ durationMs: 119_999 }) }), NONE),
		).toBeNull();
		// 标记存在被跳过的运行不算慢
		expect(
			selectEnvBanner(
				input({ lastSetup: run({ durationMs: 300_000, setupRan: false }) }),
				NONE,
			),
		).toBeNull();
	});

	it("缺少配置：有仓库、setup 成功过、仓库与项目都没有脚本", () => {
		const bare = run({ hasRepoScript: false, hasProjectScript: false });
		expect(selectEnvBanner(input({ lastSetup: bare }), NONE)).toMatchObject({
			kind: "missing_config",
		});
		expect(
			selectEnvBanner(input({ lastSetup: bare }, { project: {} }), NONE),
		).toBeNull();
		expect(
			selectEnvBanner(
				input({ lastSetup: { ...bare, hasProjectScript: true } }),
				NONE,
			),
		).toBeNull();
		// 失败的运行不算「完成过一次」，显示的是失败提示
		expect(
			selectEnvBanner(input({ lastSetup: { ...bare, ok: false } }), NONE),
		).toMatchObject({ kind: "run_failed" });
	});

	it("orb 失败压过其它提示且不可关闭，原因优先取线程错误", () => {
		const i = input(
			{
				state: "failed",
				failureReason: "orb 原因",
				lastSetup: run({ ok: false }),
			},
			{
				thread: {
					state: "error",
					error: { kind: "orb_failed", reason: "线程原因" },
				},
			},
		);
		expect(selectEnvBanner(i, new Set(["orb_failed"]))).toEqual({
			kind: "orb_failed",
			key: "orb_failed",
			reason: "线程原因",
		});
		expect(
			selectEnvBanner(input({ state: "failed", failureReason: "r" }), NONE),
		).toMatchObject({ kind: "orb_failed", reason: "r" });
	});

	it("orb 已删除时不再提示环境问题", () => {
		expect(
			selectEnvBanner(
				input({ state: "deleted", lastSetup: run({ ok: false }) }),
				NONE,
			),
		).toBeNull();
	});
});

describe("修复提示词", () => {
	const log = Array.from({ length: 120 }, (_, i) => `line-${i + 1}`).join("\n");

	it("失败提示词包含技能名、阶段、错误与日志最后 80 行", () => {
		const r = run({ ok: false, error: "E: Unable to locate package foo" });
		const text = buildEnvPrompt(
			{ kind: "run_failed", key: "failed:osr_a", run: r },
			`${log}\n`,
			"main",
		);
		expect(text).toContain("`oryn-setup`");
		expect(text).toContain("**setup**");
		expect(text).toContain("E: Unable to locate package foo");
		expect(text).toContain("line-41\n");
		expect(text).toContain("line-120");
		expect(text).not.toContain("line-40\n");
		expect(text).toContain("`oryn orb setup run`");
		expect(text).toContain("`main`");
	});

	it("resume 失败让 agent 用 --resume 重跑", () => {
		const text = buildEnvPrompt(
			{
				kind: "run_failed",
				key: "k",
				run: run({ phase: "resume", ok: false, error: "gh: not logged in" }),
			},
			"x",
		);
		expect(text).toContain("**resume**");
		expect(text).toContain("oryn orb setup run --resume");
		expect(text).toContain(".oryn/resume");
	});

	it("过慢与缺少配置也点名技能", () => {
		const slow = buildEnvPrompt(
			{ kind: "slow_setup", key: "k", run: run({ durationMs: 300_000 }) },
			log,
		);
		expect(slow).toContain("`oryn-setup`");
		expect(slow).toContain("5.0 分钟");
		expect(slow).toContain("line-120");
		const missing = buildEnvPrompt(
			{ kind: "missing_config", key: "k", run: run() },
			"",
		);
		expect(missing).toContain("`oryn-setup`");
		expect(missing).toContain(".oryn/setup");
	});

	it("日志尾部：忽略结尾空行，超长时只保留末尾", () => {
		expect(tailLines("a\nb\nc\n\n", 2)).toBe("b\nc");
		const long = "x".repeat(50_000);
		const tail = tailLines(long, LOG_TAIL_LINES);
		expect(tail.length).toBeLessThan(long.length);
		expect(long.endsWith(tail)).toBe(true);
	});
});

describe("项目最近一次 setup", () => {
	it("取同一项目各 orb 中最晚结束的 setup", () => {
		const m = latestSetupByProject([
			{ projectId: "p1", lastSetup: run({ id: "old", finishedAt: 1 }) },
			{
				projectId: "p1",
				lastSetup: run({ id: "new", finishedAt: 5, hasRepoScript: false }),
			},
			{ projectId: "p2" },
			{ lastSetup: run({ id: "orphan" }) },
		]);
		expect(m.get("p1")?.id).toBe("new");
		expect(m.has("p2")).toBe(false);
		expect(m.size).toBe(1);
	});
});
