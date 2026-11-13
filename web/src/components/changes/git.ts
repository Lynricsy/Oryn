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

export interface GitTarget {
	orbId: string;
	/** 仓库所在目录；缺省 = orb 当前工作目录 */
	root?: string;
	scope: ChangeScope;
}

function scopeQuery({ root, scope }: GitTarget) {
	return {
		root,
		scope: scope.scope,
		commit: scope.scope === "commit" ? scope.sha : undefined,
	};
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
	orbId: string,
	root: string | undefined,
	bg = false,
	signal?: AbortSignal,
): Promise<Schemas["GitCommitList"]> {
	return unwrap(
		api.GET("/api/v1/orbs/{id}/git/commits", {
			params: { path: { id: orbId }, query: { root, bg: bg || undefined } },
			signal: requestSignal(signal),
		}),
	);
}

/** 文件级暂存 / 取消暂存；重命名把新旧路径一起提交，否则只暂存了一半。 */
export async function setStaged(
	orbId: string,
	root: string | undefined,
	files: GitFileChange[],
	staged: boolean,
): Promise<void> {
	const paths = files.flatMap((f) =>
		f.oldPath ? [f.oldPath, f.path] : [f.path],
	);
	if (paths.length === 0) return;
	const body = { root, paths };
	const params = { path: { id: orbId } };
	await unwrap(
		staged
			? api.POST("/api/v1/orbs/{id}/git/stage", { params, body })
			: api.POST("/api/v1/orbs/{id}/git/unstage", { params, body }),
	);
}
