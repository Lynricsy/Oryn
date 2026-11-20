/**
 * Pure helpers shared by tool renderers. Host-agnostic; no DOM beyond
 * `globalThis` feature probes, no host package imports.
 */
import { normalizeLanguage } from "../lib/highlight";
import type { ToolResultImage, ToolResultLike } from "./types";

export function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** String passthrough; anything else (including null/undefined) → null. */
export function str(value: unknown): string | null {
	return typeof value === "string" ? value : null;
}

/** A trailing `:chunk` that reads as a selector: line ranges, `raw`, `conflicts`. */
const SEL_CHUNK_RE = /^(raw|conflicts|\d+(?:[-+]\d*)?(?:,\d+(?:[-+]\d*)?)*)$/i;

/** Split a `path:sel` argument (handles compound `:50-100:raw` selectors). */
export function splitPathSel(rawPath: string): {
	path: string;
	sel: string | null;
} {
	let path = rawPath;
	const chunks: string[] = [];
	for (let i = 0; i < 2; i++) {
		const idx = path.lastIndexOf(":");
		if (idx <= 0 || !SEL_CHUNK_RE.test(path.slice(idx + 1))) break;
		chunks.unshift(path.slice(idx + 1));
		path = path.slice(0, idx);
	}
	return { path, sel: chunks.length > 0 ? chunks.join(":") : null };
}

/**
 * 工具调用的「主体」（右键菜单的复制命令 / 复制路径、在文件面板打开）：bash 取命令，带路径参数的取路径
 * （`read` 的 `path:选择器` 去掉选择器）；其余为 null。
 */
export function toolSubject(
	name: string,
	args: unknown,
): { kind: "command" | "path"; text: string } | null {
	if (!isRecord(args)) return null;
	const command = str(args.command);
	if (name === "bash" && command) return { kind: "command", text: command };
	const raw = str(args.path) ?? str(args.file_path);
	if (!raw) return null;
	const path = name === "read" ? splitPathSel(raw).path : raw;
	return path ? { kind: "path", text: path } : null;
}

export function num(value: unknown): number | null {
	return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/** Coerce unknown to a display string ("" for null/undefined). */
export function display(value: unknown): string {
	if (value == null) return "";
	if (typeof value === "string") return value;
	if (typeof value === "number" || typeof value === "boolean")
		return String(value);
	try {
		return JSON.stringify(value) ?? "";
	} catch {
		return String(value);
	}
}

/** Replace `/Users/<x>` / `/home/<x>` prefix with `~` for display. */
export function shortenPath(p: string): string {
	for (const prefix of ["/Users/", "/home/"]) {
		if (p.startsWith(prefix)) {
			const rest = p.slice(prefix.length);
			const slash = rest.indexOf("/");
			return slash < 0 ? "~" : `~${rest.slice(slash)}`;
		}
	}
	return p;
}

/**
 * Search scope for display: the current `path` argument (else the legacy
 * `paths`), normalized from a single string, a JSON-encoded string array
 * (`'["a.ts","b.ts"]'`), or an actual array into a flat `string[]`. Mirrors the
 * coding-agent `toPathList` so web cards render the same scope the tool searched.
 */
export function scopePaths(args: Record<string, unknown>): string[] {
	const raw = args.path ?? args.paths;
	if (typeof raw === "string") {
		const trimmed = raw.trim();
		if (trimmed.startsWith("[") && trimmed.endsWith("]")) {
			try {
				const parsed: unknown = JSON.parse(trimmed);
				if (
					Array.isArray(parsed) &&
					parsed.every((p): p is string => typeof p === "string")
				) {
					return parsed;
				}
			} catch {
				// Not valid JSON — treat the whole string as one path.
			}
		}
		return [raw];
	}
	if (Array.isArray(raw))
		return raw.filter((p): p is string => typeof p === "string");
	return [];
}

export function truncate(s: string, maxLen = 100): string {
	return s.length <= maxLen ? s : `${s.slice(0, maxLen)}…`;
}

/** Collapse all whitespace runs to single spaces (for one-line summaries). */
export function normalizeWs(s: string): string {
	return s.replace(/\s+/g, " ").trim();
}

export function replaceTabs(s: string): string {
	return s.replace(/\t/g, "   ");
}

// Control Sequence Introducer + OSC escapes.
const ANSI_RE = /\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))/g;

export function stripAnsi(s: string): string {
	return s.replace(ANSI_RE, "");
}

/** 没有（或不靠）扩展名识别的文件名。 */
const BASENAME_TO_LANG: Record<string, string> = {
	dockerfile: "dockerfile",
	containerfile: "dockerfile",
	makefile: "makefile",
	gnumakefile: "makefile",
	"cmakelists.txt": "cmake",
	".bashrc": "bash",
	".bash_profile": "bash",
	".profile": "bash",
	".zshrc": "bash",
	".envrc": "bash",
	pkgbuild: "bash",
	".env": "properties",
	".gitconfig": "ini",
	".editorconfig": "ini",
	gemfile: "ruby",
	rakefile: "ruby",
	"go.mod": "go",
	"nginx.conf": "nginx",
	"flake.lock": "json",
};

/** 由文件路径推断 highlight.js 语言（扩展名经 `normalizeLanguage` 的别名表归一）；未知返回 null。 */
export function languageFromPath(filePath: string): string | null {
	const base = (filePath.split("/").pop() ?? "").toLowerCase();
	if (Object.hasOwn(BASENAME_TO_LANG, base))
		return BASENAME_TO_LANG[base] ?? null;
	if (base.startsWith("dockerfile.")) return "dockerfile";
	if (base.startsWith(".env.")) return "properties";
	const dot = base.lastIndexOf(".");
	return dot > 0 ? normalizeLanguage(base.slice(dot + 1)) : null;
}

/** 路径末尾的读取选择器（`:50-100`、`:raw`）不属于文件名。 */
const SEL_SUFFIX_RE = /:(?:\d[\d,+-]*|raw|conflicts)$/i;

/**
 * 一组路径共同的高亮语言（`;` 分隔的多路径逐个算）；任一路径认不出语言或语言不一时为 null：
 * 混合语言的搜索输出不高亮。
 */
export function sharedLanguage(paths: readonly string[]): string | null {
	let lang: string | null = null;
	for (const entry of paths) {
		for (const part of entry.split(";")) {
			const p = part.trim().replace(SEL_SUFFIX_RE, "");
			if (!p) continue;
			const l = languageFromPath(p);
			if (l === null || (lang !== null && l !== lang)) return null;
			lang = l;
		}
	}
	return lang;
}

/** 搜索结果命中的文件：`details.files`，否则 `details.fileMatches[].path`。 */
export function resultFiles(result: ToolResultLike | undefined): string[] {
	const details = detailsRecord(result);
	if (!details) return [];
	const files: string[] = [];
	if (Array.isArray(details.files)) {
		for (const f of details.files) if (typeof f === "string") files.push(f);
	}
	if (files.length === 0 && Array.isArray(details.fileMatches)) {
		for (const m of details.fileMatches) {
			const p = isRecord(m) ? str(m.path) : null;
			if (p) files.push(p);
		}
	}
	return files;
}

/** Joined text blocks of a tool result ("" when absent). */
export function resultTextOf(result: ToolResultLike | undefined): string {
	if (!result) return "";
	const parts: string[] = [];
	for (const block of result.content) {
		if (
			block.type === "text" &&
			typeof (block as { text?: unknown }).text === "string"
		) {
			parts.push((block as { text: string }).text);
		}
	}
	return parts.join("\n");
}

export function resultImagesOf(
	result: ToolResultLike | undefined,
): ToolResultImage[] {
	if (!result) return [];
	const images: ToolResultImage[] = [];
	for (const block of result.content) {
		const img = block as Partial<ToolResultImage>;
		if (
			block.type === "image" &&
			typeof img.data === "string" &&
			typeof img.mimeType === "string"
		) {
			images.push(img as ToolResultImage);
		}
	}
	return images;
}

/** `result.details` when it is a plain object; renderers narrow field-by-field. */
export function detailsRecord(
	result: ToolResultLike | undefined,
): Record<string, unknown> | null {
	return result && isRecord(result.details) ? result.details : null;
}

/** Compact one-line JSON digest of arbitrary args (generic summary fallback). */
export function argsDigest(args: unknown, maxLen = 96): string {
	if (args == null) return "";
	if (isRecord(args) && Object.keys(args).length === 0) return "";
	return truncate(normalizeWs(display(args)), maxLen);
}
