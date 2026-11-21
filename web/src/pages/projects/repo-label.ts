/** 仓库地址的展示（M24）：项目总览、项目主页的仓库行用。 */

/** `https://github.com/acme/w.git` → `github.com/acme/w`；解析不了的地址（如 `git@host:o/r`）原样返回。 */
export function repoLabel(url: string): string {
	let u: URL;
	try {
		u = new URL(url);
	} catch {
		return url;
	}
	// `github.com:o/r` 会被解析成 scheme `github.com:`，host 为空
	if (!u.host) return url;
	return (u.host + u.pathname).replace(/\/+$/, "").replace(/\.git$/, "");
}

/** 仓库是否在 github.com 上。 */
export function isGitHubRepo(url?: string): boolean {
	if (!url) return false;
	try {
		return new URL(url).host.toLowerCase() === "github.com";
	} catch {
		return false;
	}
}
