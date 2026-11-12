/**
 * 线程页环境提示条（计划 I.7）：选哪条提示、按「线程 + 运行记录」关闭、给 agent 的修复提示词。
 * 纯函数，组件只负责渲染与发送。
 */
import type { Orb, Project, Schemas, Thread } from "../api/client";

export type SetupRun = Schemas["OrbSetupRun"];

/** setup 耗时达到该值即提示「让 agent 优化」。 */
export const SLOW_SETUP_MS = 120_000;
/** 修复提示词里附带的日志行数。 */
export const LOG_TAIL_LINES = 80;
/** 日志尾部字符上限：极长的单行不会把提示词撑爆。 */
const LOG_TAIL_MAX_CHARS = 16_000;

export type EnvBanner =
	| { kind: "orb_failed"; key: string; reason: string }
	| { kind: "run_failed"; key: string; run: SetupRun }
	| { kind: "slow_setup"; key: string; run: SetupRun }
	| { kind: "missing_config"; key: string; run: SetupRun };

export interface EnvBannerInput {
	thread: Pick<Thread, "error" | "state"> | undefined;
	orb:
		| Pick<Orb, "state" | "failureReason" | "lastSetup" | "lastResume">
		| undefined;
	project: Pick<Project, "repoUrl"> | undefined;
}

/**
 * 按优先级列出当前适用的提示条（第一条之外的在前者被关闭后才显示）：
 * orb 失败 → setup 失败 → resume 失败 → setup 过慢 → 缺少环境配置。
 * orb 失败时只给出它本身（其余提示基于已不可用的环境，没有意义）。
 */
export function envBannerCandidates(input: EnvBannerInput): EnvBanner[] {
	const { thread, orb, project } = input;
	const orbFailed =
		thread?.error?.kind === "orb_failed" || orb?.state === "failed";
	if (orbFailed) {
		const reason = thread?.error?.reason || orb?.failureReason || "";
		return [{ kind: "orb_failed", key: "orb_failed", reason }];
	}
	if (!orb || orb.state === "deleted" || orb.state === "deleting") return [];
	const out: EnvBanner[] = [];
	const setup = orb.lastSetup;
	const resume = orb.lastResume;
	if (setup && !setup.ok)
		out.push({ kind: "run_failed", key: `failed:${setup.id}`, run: setup });
	if (resume && !resume.ok)
		out.push({ kind: "run_failed", key: `failed:${resume.id}`, run: resume });
	if (setup?.ok && setup.setupRan && setup.durationMs >= SLOW_SETUP_MS)
		out.push({ kind: "slow_setup", key: `slow:${setup.id}`, run: setup });
	if (
		project?.repoUrl &&
		setup?.ok &&
		!setup.hasRepoScript &&
		!setup.hasProjectScript
	)
		out.push({
			kind: "missing_config",
			key: `missing:${setup.id}`,
			run: setup,
		});
	return out;
}

/** 当前应显示的提示条：第一条未被关闭的候选。orb 失败不可关闭（需要用户处理）。 */
export function selectEnvBanner(
	input: EnvBannerInput,
	dismissed: ReadonlySet<string>,
): EnvBanner | null {
	for (const b of envBannerCandidates(input)) {
		if (b.kind === "orb_failed" || !dismissed.has(b.key)) return b;
	}
	return null;
}

// ---------------------------------------------------------------- 关闭记录

const STORAGE_PREFIX = "oryn:env-banner-dismissed:";
/** 每个线程最多记住的关闭项，旧的先淘汰。 */
const MAX_DISMISSED = 20;

function storage(): Storage | null {
	try {
		return typeof localStorage === "undefined" ? null : localStorage;
	} catch {
		return null;
	}
}

export function loadDismissed(threadId: string): Set<string> {
	const raw = storage()?.getItem(STORAGE_PREFIX + threadId);
	if (!raw) return new Set();
	try {
		const list: unknown = JSON.parse(raw);
		return new Set(
			Array.isArray(list)
				? list.filter((k): k is string => typeof k === "string")
				: [],
		);
	} catch {
		return new Set();
	}
}

export function dismissBanner(threadId: string, key: string): Set<string> {
	const next = [...loadDismissed(threadId)].filter((k) => k !== key);
	next.push(key);
	const kept = next.slice(-MAX_DISMISSED);
	try {
		storage()?.setItem(STORAGE_PREFIX + threadId, JSON.stringify(kept));
	} catch {
		// 存储不可用（隐私模式 / 配额满）：本次会话内仍按内存状态关闭
	}
	return new Set(kept);
}

// ---------------------------------------------------------------- 提示词

/** 日志最后 n 行（忽略结尾换行），并限制总长度只保留末尾。 */
export function tailLines(text: string, n = LOG_TAIL_LINES): string {
	const lines = text.replace(/\s+$/u, "").split("\n");
	const tail = lines.slice(-n).join("\n");
	return tail.length > LOG_TAIL_MAX_CHARS
		? tail.slice(tail.length - LOG_TAIL_MAX_CHARS)
		: tail;
}

function minutes(ms: number): string {
	const m = ms / 60_000;
	return m >= 10 ? String(Math.round(m)) : m.toFixed(1);
}

function logBlock(log: string): string {
	const tail = tailLines(log);
	if (!tail) return "（日志为空）";
	// 日志里可能本身含 ``` ，用更长的围栏
	return `\`\`\`\`text\n${tail}\n\`\`\`\``;
}

/** 「让 agent 修复 / 优化 / 生成」发送的提示词（中文；点名 `oryn-setup` 技能）。 */
export function buildEnvPrompt(
	banner: Exclude<EnvBanner, { kind: "orb_failed" }>,
	log: string,
	baseBranch?: string,
): string {
	const run = banner.run;
	const branch = baseBranch ? `基线分支 \`${baseBranch}\`` : "项目的基线分支";
	switch (banner.kind) {
		case "run_failed": {
			const phase = run.phase;
			return [
				`这台 orb 的环境配置在 **${phase}** 阶段失败了（触发方式：${run.trigger === "manual" ? "手动重跑" : "自动"}，耗时 ${minutes(run.durationMs)} 分钟）。`,
				"",
				`错误：${run.error || "（未给出错误信息）"}`,
				"",
				`${phase} 日志最后 ${LOG_TAIL_LINES} 行：`,
				logBlock(log),
				"",
				`请使用 \`oryn-setup\` 技能排查并修复：找出失败原因，修改 \`.oryn/${phase}\`（仓库里已有 \`.agents/${phase}\` 则原地修改）或项目的 ${phase} 脚本，然后运行 \`oryn orb setup run${phase === "resume" ? " --resume" : ""}\` 重跑直到成功${phase === "setup" ? "（再跑一次确认可重复执行）" : ""}。`,
				`仓库内脚本的改动需要提交并推送到${branch}，以后的新 orb 才会生效；未推送前不要宣称完成。`,
			].join("\n");
		}
		case "slow_setup":
			return [
				`这台 orb 的 setup 耗时 ${minutes(run.durationMs)} 分钟，太慢了。`,
				"",
				`setup 日志最后 ${LOG_TAIL_LINES} 行：`,
				logBlock(log),
				"",
				"请使用 `oryn-setup` 技能优化环境配置：找出最耗时的步骤，改用锁文件 / 缓存 / 预编译产物 / 并行安装，跳过已完成的步骤，长期进程不要放进 setup。",
				`改完后运行 \`oryn orb setup run\` 两次并分别计时（冷 / 暖），确认结果正确且更快，再提交并推送到${branch}。`,
			].join("\n");
		case "missing_config":
			return [
				"这个项目还没有 Oryn orb 环境配置（仓库里没有 `.oryn/setup`，项目设置里也没有 setup 脚本）。",
				"",
				"请使用 `oryn-setup` 技能为它生成环境配置：调查仓库（锁文件、版本钉、env 模板、数据库、CI 安装步骤、已有脚本），写出 `.oryn/setup`（需要时再写 `.oryn/resume`）并 `chmod +x`，",
				`在当前 orb 里用 \`oryn orb setup run\` 跑两次验证（第二次应明显更快），用干净的 \`bash -lc\` 确认环境可用，然后提交并推送到${branch}。`,
			].join("\n");
	}
}

/** 每个项目最近一次 setup 运行（取各 orb 的 `lastSetup` 中最晚结束的），项目设置页显示「仓库内脚本：有 / 无」用。 */
export function latestSetupByProject(
	orbs: readonly Pick<Orb, "projectId" | "lastSetup">[],
): Map<string, SetupRun> {
	const out = new Map<string, SetupRun>();
	for (const o of orbs) {
		if (!o.projectId || !o.lastSetup) continue;
		const prev = out.get(o.projectId);
		if (!prev || o.lastSetup.finishedAt > prev.finishedAt)
			out.set(o.projectId, o.lastSetup);
	}
	return out;
}
