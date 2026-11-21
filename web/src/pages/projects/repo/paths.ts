/** 仓库浏览的路径工具（M24 阶段 ②）：README 等文档里相对地址的解析，以及文件页的站内地址。 */
import { repoBlobUrl } from "../../../api/repo-queries";
import type { RelativeUrls } from "../../../components/markdown/Markdown";

/**
 * 把文档里的相对地址解析成仓库内路径：先拆掉 `?query` 与 `#hash`；`/` 开头时相对仓库根（GitHub 的语义），
 * 否则相对 `baseDir`（仓库内目录，根为 ""）；归一化 `.` 与 `..`，各段按百分号编码解码。
 * 越过仓库根或编码无效时返回 null。`hash` 含 `#`，没有时为空串。
 */
export function resolveRepoPath(
	baseDir: string,
	rel: string,
): { path: string; hash: string } | null {
	const hashAt = rel.indexOf("#");
	const hash = hashAt >= 0 ? rel.slice(hashAt) : "";
	const noHash = hashAt >= 0 ? rel.slice(0, hashAt) : rel;
	const queryAt = noHash.indexOf("?");
	const bare = queryAt >= 0 ? noHash.slice(0, queryAt) : noHash;
	const out = bare.startsWith("/")
		? []
		: baseDir.split("/").filter((s) => s !== "");
	for (const raw of bare.split("/")) {
		let seg: string;
		try {
			seg = decodeURIComponent(raw);
		} catch {
			return null;
		}
		if (seg === "" || seg === ".") continue;
		if (seg === "..") {
			if (out.length === 0) return null;
			out.pop();
			continue;
		}
		out.push(seg);
	}
	return { path: out.join("/"), hash };
}

/** 仓库文件页（目录或文件）的站内地址；路径逐段编码（文件名里的 `#`、`?` 不会被当成锚点或查询）；ref 为空时由文件页取基线分支。 */
export function repoFileHref(
	projectId: string,
	path: string,
	ref?: string,
): string {
	const segs = path.split("/").map(encodeURIComponent).join("/");
	return `/projects/${projectId}/files/${segs}${ref ? `?ref=${encodeURIComponent(ref)}` : ""}`;
}

/**
 * 仓库里一篇 Markdown 的相对地址解析（`Markdown` 的 `relativeTo`）：`baseDir` 是文档所在目录（根为 ""）；
 * 链接进文件页（带锚点），图片取文件内容地址；越过仓库根的原样输出。
 */
export function repoRelativeUrls(
	projectId: string,
	ref: string,
	baseDir: string,
): RelativeUrls {
	return {
		link: (href) => {
			const r = resolveRepoPath(baseDir, href);
			return r && repoFileHref(projectId, r.path, ref) + r.hash;
		},
		image: (src) => {
			const r = resolveRepoPath(baseDir, src);
			return r && repoBlobUrl(projectId, ref, r.path);
		},
	};
}
