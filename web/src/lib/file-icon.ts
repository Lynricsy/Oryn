/**
 * 按文件名、目录名、条目类型或 MIME 解析内置文件类型图标键：
 * 主集合为 Catppuccin Icons（随主题自动切 Latte / Mocha），缺项回落至 Material Icon Theme（`m:*`）。
 * 供文件树、文件查看器、上传队列、附件条、消息附件、仓库目录、归档预览、工具输出等共用。
 */
import {
	FILE_TYPE_ICONS,
	type FileTypeIconKey,
} from "../components/ui/file-type-icons.gen";
import type { IconName } from "../components/ui/icons";
import {
	EXACT_FILE_ICONS,
	EXT_FILE_ICONS,
	FOLDER_NAME_ICONS,
} from "./file-icon-map.gen";

export type FileEntryKind = "file" | "dir" | "symlink" | "submodule";

/** 从路径中提取不含首尾斜杠的文件/目录基名（小写）。 */
function baseLower(pathOrName: string): string {
	const trimmed = pathOrName.replace(/\/+$/, "");
	const slash = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
	return (slash >= 0 ? trimmed.slice(slash + 1) : trimmed).toLowerCase();
}

/** 根据 MIME 主类型兜底挑选图标键。 */
function iconFromMime(mime?: string): FileTypeIconKey | null {
	if (!mime) return null;
	const clean = mime.split(";")[0]?.trim().toLowerCase() ?? "";
	if (!clean) return null;
	if (clean === "image/svg+xml") return "svg";
	if (clean.startsWith("image/")) return "image";
	if (clean.startsWith("video/")) return "video";
	if (clean.startsWith("audio/")) return "audio";
	if (clean.startsWith("font/")) return "font";
	if (clean === "application/pdf") return "pdf";
	if (clean === "application/json" || clean.endsWith("+json")) return "json";
	if (
		clean === "application/xml" ||
		clean === "text/xml" ||
		clean.endsWith("+xml")
	)
		return "xml";
	if (clean === "text/markdown") return "markdown";
	if (clean === "text/csv" || clean === "text/tab-separated-values")
		return "csv";
	if (clean === "text/html") return "html";
	if (clean === "text/css") return "css";
	if (
		clean === "text/javascript" ||
		clean === "application/javascript" ||
		clean === "application/ecmascript"
	)
		return "javascript";
	if (clean === "application/typescript" || clean === "text/typescript")
		return "typescript";
	if (
		clean === "application/yaml" ||
		clean === "text/yaml" ||
		clean === "application/x-yaml"
	)
		return "yaml";
	if (clean === "application/toml") return "toml";
	if (clean === "application/x-sh" || clean === "text/x-shellscript")
		return "bash";
	if (clean === "application/sql" || clean === "application/vnd.sqlite3")
		return "database";
	if (clean === "application/epub+zip") return "m:epub";
	if (
		clean === "application/zip" ||
		clean === "application/gzip" ||
		clean === "application/x-tar" ||
		clean === "application/x-7z-compressed" ||
		clean === "application/x-rar-compressed" ||
		clean === "application/x-bzip2" ||
		clean === "application/x-xz" ||
		clean === "application/zstd"
	)
		return "zip";
	if (
		clean === "application/msword" ||
		clean ===
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	)
		return "ms-word";
	if (
		clean === "application/vnd.ms-excel" ||
		clean ===
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	)
		return "ms-excel";
	if (
		clean === "application/vnd.ms-powerpoint" ||
		clean ===
			"application/vnd.openxmlformats-officedocument.presentationml.presentation"
	)
		return "ms-powerpoint";
	if (clean === "application/wasm") return "web-assembly";
	if (clean.startsWith("text/")) return "text";
	return null;
}

/** 常见前缀模式（如 `.env.production`、`Dockerfile.dev`、`docker-compose.prod.yml`）。 */
function iconFromPattern(base: string): FileTypeIconKey | null {
	if (base === ".env" || base.startsWith(".env.")) return "env";
	if (base.startsWith("dockerfile.") || base.endsWith(".dockerfile"))
		return "docker";
	if (
		(base.startsWith("docker-compose.") || base.startsWith("compose.")) &&
		(base.endsWith(".yml") || base.endsWith(".yaml"))
	)
		return "docker-compose";
	if (base.startsWith("readme.") || base === "readme") return "readme";
	if (base.startsWith("license") || base.startsWith("licence"))
		return "license";
	if (base.startsWith("changelog")) return "changelog";
	if (base.startsWith("makefile.") || base === "gnumakefile") return "makefile";
	if (base.startsWith("tsconfig.") && base.endsWith(".json"))
		return "typescript-config";
	if (base.startsWith("jsconfig.") && base.endsWith(".json"))
		return "javascript-config";
	if (base.startsWith("vite.config.")) return "vite";
	if (base.startsWith("vitest.config.")) return "vitest";
	if (base.startsWith("tailwind.config.")) return "tailwind";
	if (base.startsWith("eslint.config.")) return "eslint";
	if (base.startsWith("prettier.config.")) return "prettier";
	if (base.startsWith("playwright.config.")) return "playwright";
	if (base.startsWith("next.config.")) return "next";
	if (base.startsWith("nuxt.config.")) return "nuxt";
	if (base.startsWith("astro.config.")) return "astro-config";
	if (base.startsWith("svelte.config.")) return "svelte-config";
	return null;
}

/**
 * 解析文件图标键：
 * 1. 精确文件名（不区分大小写）
 * 2. 常见前缀/模式（`.env.*`、`Dockerfile.*`、`vite.config.*` 等）
 * 3. 从长到短的复合/单扩展名（如 `.test.tsx` → `.tsx`、`.tar.gz` → `.gz`）
 * 4. MIME 类型兜底
 * 5. 默认 `_file`
 */
export function resolveFileIconKey(
	name: string,
	mime?: string,
): FileTypeIconKey {
	const base = baseLower(name);
	if (base) {
		if (Object.hasOwn(EXACT_FILE_ICONS, base)) {
			const exact = EXACT_FILE_ICONS[base];
			if (exact && Object.hasOwn(FILE_TYPE_ICONS, exact))
				return exact as FileTypeIconKey;
		}

		const pattern = iconFromPattern(base);
		if (pattern && Object.hasOwn(FILE_TYPE_ICONS, pattern)) return pattern;

		// 逐段尝试复合后缀（跳过第 0 位点号，避免把 `.gitignore` 整段当后缀）
		let dot = base.indexOf(".", 1);
		while (dot !== -1 && dot < base.length - 1) {
			const ext = base.slice(dot + 1);
			if (Object.hasOwn(EXT_FILE_ICONS, ext)) {
				const hit = EXT_FILE_ICONS[ext];
				if (hit && Object.hasOwn(FILE_TYPE_ICONS, hit))
					return hit as FileTypeIconKey;
			}
			dot = base.indexOf(".", dot + 1);
		}
	}
	const byMime = iconFromMime(mime);
	if (byMime && Object.hasOwn(FILE_TYPE_ICONS, byMime)) return byMime;
	return "_file";
}

/**
 * 解析目录图标键：优先匹配语义文件夹（Catppuccin 主集 + Material 兜底），支持展开/收起态。
 */
export function resolveFolderIconKey(
	name: string,
	open = false,
): FileTypeIconKey {
	const base = baseLower(name);
	const matched =
		base && Object.hasOwn(FOLDER_NAME_ICONS, base)
			? FOLDER_NAME_ICONS[base]
			: undefined;
	if (matched) {
		if (open) {
			const openKey = matched.startsWith("m:")
				? `${matched}-open`
				: `${matched}_open`;
			if (Object.hasOwn(FILE_TYPE_ICONS, openKey))
				return openKey as FileTypeIconKey;
		}
		if (Object.hasOwn(FILE_TYPE_ICONS, matched))
			return matched as FileTypeIconKey;
	}
	return open ? "_folder_open" : "_folder";
}

/** 统一解析任意文件系统条目（文件 / 目录 / 软链 / 子模块）的图标键。 */
export function resolveEntryIconKey(
	name: string,
	kind: FileEntryKind = "file",
	open = false,
	mime?: string,
): FileTypeIconKey {
	if (kind === "dir") return resolveFolderIconKey(name, open);
	if (kind === "symlink") return "symlink";
	if (kind === "submodule") return "folder_git";
	return resolveFileIconKey(name, mime);
}

const CODE_EXT =
	/\.(c|cc|cpp|cs|css|go|h|hpp|html|java|js|jsx|json|kt|lua|mjs|php|py|rb|rs|scss|sh|sql|svelte|swift|toml|ts|tsx|vue|xml|ya?ml|zig)$/i;
const IMAGE_EXT = /\.(avif|bmp|gif|heic|ico|jpe?g|png|svg|webp)$/i;
const TEXT_EXT = /\.(csv|log|md|markdown|mdx|rst|txt)$/i;

/** 旧单色 Phosphor 图标名映射（保留给仅接受 `IconName` 的兼容位点）。 */
export function fileIcon(name: string, mime?: string): IconName {
	if (mime?.startsWith("image/") || IMAGE_EXT.test(name)) return "fileImage";
	if (CODE_EXT.test(name)) return "fileCode";
	if (mime?.startsWith("text/") || TEXT_EXT.test(name)) return "fileText";
	return "file";
}
