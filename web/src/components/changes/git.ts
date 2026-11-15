/** Changes 面板用到的 orb git 接口（`/api/v1/orbs/{id}/git/*`）。 */
import { api, type Schemas, unwrap } from "../../api/client";

export type GitStatus = Schemas["GitStatus"];
export type GitFileChange = Schemas["GitFileChange"];
export type GitFileDiff = Schemas["GitFileDiff"];
export type GitCommit = Schemas["GitCommit"];
export type GitScope = Schemas["GitScope"];

/** 当前查看的范围；`commit` 时带 sha。 */
export type ChangeScope =
	| { scope: "all" | "uncommitted" }
	| { scope: "commit"; sha: string };

/** 一个仓库：在哪台 orb、以哪个线程的身份（runner 线程的范围是它的工作目录）、哪个目录、对比哪个基线。 */
export interface GitRepo {
	orbId: string;
	/** 当前线程：请求都带 `thread`（runner 的 orb 必须带，控制面据此转发线程的工作目录） */
	threadId: string;
	/** 仓库所在目录；缺省 = 线程工作目录（普通 orb 为 orb 当前工作目录） */
	root?: string;
	/** 基线分支名；缺省由服务端按项目 `baseBranch` / `origin/HEAD` / `main` / `master` 推定 */
	base?: string;
}

export interface GitTarget extends GitRepo {
	scope: ChangeScope;
}

function scopeQuery({ threadId, root, base, scope }: GitTarget) {
	return {
		thread: threadId,
		root,
		base,
		scope: scope.scope,
		commit: scope.scope === "commit" ? scope.sha : undefined,
	};
}

/** 与 `GitBaseQuery` 的 pattern 一致：先在前端拦下服务端必然拒绝的分支名。 */
const BRANCH_RE = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$/;

export function isValidBranch(name: string): boolean {
	return BRANCH_RE.test(name);
}

/** 线程在改动面板里选定的仓库目录（`repo=false` 时从候选里挑的）存在 localStorage 的键。 */
export function changesRootKey(threadId: string): string {
	return `oryn.changes.root.${threadId}`;
}

/** 读取线程选定的仓库目录；未选或 localStorage 不可用时为 `undefined`。 */
export function readChangesRoot(threadId: string): string | undefined {
	try {
		return localStorage.getItem(changesRootKey(threadId)) ?? undefined;
	} catch {
		return undefined;
	}
}

/** 单次 git 请求的上限；超时以 `TimeoutError` 结束（服务端 45s 后回 504）。 */
export const GIT_REQUEST_TIMEOUT_MS = 30_000;

/** 调用方的取消信号（面板卸载）叠加请求超时。 */
export function requestSignal(signal?: AbortSignal): AbortSignal {
	const timeout = AbortSignal.timeout(GIT_REQUEST_TIMEOUT_MS);
	return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

/** 请求被取消或超时（而不是服务端报错）。 */
export function isAbort(e: unknown): boolean {
	return (
		e instanceof DOMException &&
		(e.name === "AbortError" || e.name === "TimeoutError")
	);
}

/** `bg=true`：后台轮询，orb 不可用时 409 `orb_not_ready`，不唤醒、不计用户交互。 */
export function getGitStatus(
	target: GitTarget,
	bg: boolean,
	signal?: AbortSignal,
): Promise<GitStatus> {
	return unwrap(
		api.GET("/api/v1/orbs/{id}/git/status", {
			params: {
				path: { id: target.orbId },
				query: { ...scopeQuery(target), bg: bg || undefined },
			},
			signal: requestSignal(signal),
		}),
	);
}

export function getGitDiff(
	target: GitTarget,
	file: Pick<GitFileChange, "path" | "oldPath">,
	bg = false,
	signal?: AbortSignal,
): Promise<GitFileDiff> {
	return unwrap(
		api.GET("/api/v1/orbs/{id}/git/diff", {
			params: {
				path: { id: target.orbId },
				query: {
					...scopeQuery(target),
					path: file.path,
					oldPath: file.oldPath,
					bg: bg || undefined,
				},
			},
			signal: requestSignal(signal),
		}),
	);
}

/** `bg=true` 仅用于后台触发的刷新（HEAD 变化）；打开与手动刷新走前台。 */
export function listGitCommits(
	{ orbId, threadId, root, base }: GitRepo,
	bg = false,
	signal?: AbortSignal,
): Promise<Schemas["GitCommitList"]> {
	return unwrap(
		api.GET("/api/v1/orbs/{id}/git/commits", {
			params: {
				path: { id: orbId },
				query: { thread: threadId, root, base, bg: bg || undefined },
			},
			signal: requestSignal(signal),
		}),
	);
}

/** 文件级暂存 / 取消暂存；重命名把新旧路径一起提交，否则只暂存了一半。 */
export async function setStaged(
	{ orbId, threadId, root }: Omit<GitRepo, "base">,
	files: GitFileChange[],
	staged: boolean,
): Promise<void> {
	const paths = files.flatMap((f) =>
		f.oldPath ? [f.oldPath, f.path] : [f.path],
	);
	if (paths.length === 0) return;
	const body = { root, paths };
	const params = { path: { id: orbId }, query: { thread: threadId } };
	await unwrap(
		staged
			? api.POST("/api/v1/orbs/{id}/git/stage", { params, body })
			: api.POST("/api/v1/orbs/{id}/git/unstage", { params, body }),
	);
}
