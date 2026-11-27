/**
 * 多文件条目（技能目录、扩展 / 工具目录）的导入与上传：把文件夹、拖放或 zip 整理成相对路径 → 字节，
 * 再转成批量变更接口（`applyAgentConfigBatch`）的文件项。
 *
 * - 跳过系统杂项：`__MACOSX/`、`.DS_Store`、`Thumbs.db`，以及任何 `.git` 目录；
 * - 相对路径一律以 `/` 分隔，拒绝空段、`.`、`..` 与绝对路径（zip 里的这类条目整条丢弃）；
 * - 合法 UTF-8 且不含 NUL 的按原文发送，其余 base64；以 `#!` 开头的文本给可执行权限（0755）。
 */
import { unzipSync } from "fflate";
import type { Schemas } from "../api/client";
import { parseFrontmatter } from "./frontmatter";

/** 条目内的一个文件：相对条目根的路径与内容。 */
export interface BundleFile {
	path: string;
	data: Uint8Array;
}

export type BatchFile = Schemas["AgentConfigBatchFile"];

const JUNK_NAMES: Record<string, true> = {
	".DS_Store": true,
	"Thumbs.db": true,
	"desktop.ini": true,
};

/** 规范化相对路径；不合法或属于系统杂项时返回 null。 */
export function cleanRelPath(raw: string): string | null {
	const p = raw.replace(/\\/g, "/");
	if (p === "" || p.startsWith("/")) return null;
	const segs = p.split("/");
	for (const s of segs) {
		if (s === "" || s === "." || s === "..") return null;
		if (s === ".git" || s === "__MACOSX") return null;
	}
	if (Object.hasOwn(JUNK_NAMES, segs[segs.length - 1] ?? "")) return null;
	return p;
}

/** 解开 zip：只取文件，按 `cleanRelPath` 过滤。 */
export function filesFromZip(data: Uint8Array): BundleFile[] {
	const out: BundleFile[] = [];
	const unzipped = unzipSync(data, {
		filter: (f) => !f.name.endsWith("/") && cleanRelPath(f.name) !== null,
	});
	for (const [name, bytes] of Object.entries(unzipped)) {
		const path = cleanRelPath(name);
		if (path) out.push({ path, data: bytes });
	}
	return out.sort((a, b) => a.path.localeCompare(b.path));
}

/** 读入 `{file, path}` 列表（拖放批次、带 `webkitRelativePath` 的文件夹选择）。 */
export async function filesFromList(
	list: readonly { file: Blob; path: string }[],
): Promise<BundleFile[]> {
	const out: BundleFile[] = [];
	for (const { file, path } of list) {
		const clean = cleanRelPath(path);
		if (!clean) continue;
		out.push({ path: clean, data: new Uint8Array(await file.arrayBuffer()) });
	}
	return out.sort((a, b) => a.path.localeCompare(b.path));
}

/** 文件夹选择框（`webkitdirectory`）的文件：路径取 `webkitRelativePath`，没有时取文件名。 */
export function pickedPaths(
	files: Iterable<File>,
): { file: File; path: string }[] {
	return [...files].map((file) => ({
		file,
		path: file.webkitRelativePath || file.name,
	}));
}

/** 所有文件都在同一个顶层目录下时去掉这一层，返回目录名；否则 root 为 null。 */
export function stripCommonRoot(files: readonly BundleFile[]): {
	root: string | null;
	files: BundleFile[];
} {
	const first = files[0]?.path.split("/")[0];
	if (
		first === undefined ||
		!files.every((f) => f.path.startsWith(`${first}/`))
	)
		return { root: null, files: [...files] };
	return {
		root: first,
		files: files.map((f) => ({ ...f, path: f.path.slice(first.length + 1) })),
	};
}

/** 收窄到唯一的一个技能目录：根下有 SKILL.md 时就是根；否则恰有一个目录含 SKILL.md 时取它。 */
export function narrowToSkill(
	files: readonly BundleFile[],
):
	| { ok: true; root: string | null; files: BundleFile[] }
	| { ok: false; candidates: string[] } {
	if (files.some((f) => f.path === "SKILL.md"))
		return { ok: true, root: null, files: [...files] };
	const dirs = files
		.filter((f) => f.path.endsWith("/SKILL.md"))
		.map((f) => f.path.slice(0, -"/SKILL.md".length));
	// 只认最浅的一层：技能目录里的示例子技能不算独立候选
	const top = dirs.filter(
		(d) => !dirs.some((o) => o !== d && d.startsWith(`${o}/`)),
	);
	const only = top.length === 1 ? top[0] : undefined;
	if (only === undefined) return { ok: false, candidates: top.sort() };
	const prefix = `${only}/`;
	return {
		ok: true,
		root: only.slice(only.lastIndexOf("/") + 1),
		files: files
			.filter((f) => f.path.startsWith(prefix))
			.map((f) => ({ ...f, path: f.path.slice(prefix.length) })),
	};
}

const decoder = new TextDecoder("utf-8", { fatal: true });

/** 合法 UTF-8 且不含 NUL 时返回文本，否则 null。 */
export function asText(data: Uint8Array): string | null {
	if (data.includes(0)) return null;
	try {
		return decoder.decode(data);
	} catch {
		return null;
	}
}

/** 标准 base64（分块拼接，避免大数组展开参数溢出）。 */
export function toBase64(data: Uint8Array): string {
	let bin = "";
	const CHUNK = 0x8000;
	for (let i = 0; i < data.length; i += CHUNK)
		bin += String.fromCharCode(...data.subarray(i, i + CHUNK));
	return btoa(bin);
}

const EXECUTABLE = 0o755;

/** 条目文件 → 批量变更的文件项，路径加上 `prefix`（如 `skills/foo`）。 */
export function toBatchFiles(
	prefix: string,
	files: readonly BundleFile[],
): BatchFile[] {
	return files.map((f): BatchFile => {
		const path = prefix ? `${prefix}/${f.path}` : f.path;
		const text = asText(f.data);
		if (text === null)
			return { path, content: toBase64(f.data), encoding: "base64" };
		return text.startsWith("#!")
			? { path, content: text, encoding: "utf8", mode: EXECUTABLE }
			: { path, content: text, encoding: "utf8" };
	});
}

/** 合计字节数。 */
export function totalBytes(files: readonly BundleFile[]): number {
	return files.reduce((n, f) => n + f.data.length, 0);
}

/** SKILL.md 的 frontmatter `description`（去空白）；读不出时为空串。 */
export function skillDescription(files: readonly BundleFile[]): string {
	const main = files.find((f) => f.path === "SKILL.md");
	const text = main ? asText(main.data) : null;
	if (text === null) return "";
	const desc = parseFrontmatter(text).data.description;
	return typeof desc === "string" ? desc.trim() : "";
}

/** 由目录名 / 文件名得出建议的条目名：`lower` 时转小写；不合规的字符换成 `-`，去掉首尾的 `-` 与 `.`。 */
export function suggestName(raw: string, lower: boolean): string {
	const base = raw.replace(/\.(zip|skill)$/i, "");
	const s = (lower ? base.toLowerCase() : base)
		.replace(lower ? /[^a-z0-9-]+/g : /[^A-Za-z0-9._-]+/g, "-")
		.replace(/^[-.]+|[-.]+$/g, "");
	return s.slice(0, 64);
}
