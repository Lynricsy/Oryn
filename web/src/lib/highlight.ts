/**
 * highlight.js 按需加载：核心与每种语言都是独立的懒加载分块，首屏不承担其体积。
 * 渲染端用 `useHighlighted`：语言未加载时先返回 null（纯文本），加载完成后自动重渲染。
 */
import type { HLJSApi, LanguageFn } from "highlight.js";
import { useEffect, useMemo, useState } from "react";

type LangLoader = () => Promise<{ default: LanguageFn }>;

const LOADERS: Record<string, LangLoader> = {
	bash: () => import("highlight.js/lib/languages/bash"),
	c: () => import("highlight.js/lib/languages/c"),
	cpp: () => import("highlight.js/lib/languages/cpp"),
	csharp: () => import("highlight.js/lib/languages/csharp"),
	css: () => import("highlight.js/lib/languages/css"),
	diff: () => import("highlight.js/lib/languages/diff"),
	dockerfile: () => import("highlight.js/lib/languages/dockerfile"),
	go: () => import("highlight.js/lib/languages/go"),
	ini: () => import("highlight.js/lib/languages/ini"),
	java: () => import("highlight.js/lib/languages/java"),
	javascript: () => import("highlight.js/lib/languages/javascript"),
	json: () => import("highlight.js/lib/languages/json"),
	kotlin: () => import("highlight.js/lib/languages/kotlin"),
	less: () => import("highlight.js/lib/languages/less"),
	lua: () => import("highlight.js/lib/languages/lua"),
	makefile: () => import("highlight.js/lib/languages/makefile"),
	markdown: () => import("highlight.js/lib/languages/markdown"),
	php: () => import("highlight.js/lib/languages/php"),
	python: () => import("highlight.js/lib/languages/python"),
	ruby: () => import("highlight.js/lib/languages/ruby"),
	rust: () => import("highlight.js/lib/languages/rust"),
	scss: () => import("highlight.js/lib/languages/scss"),
	sql: () => import("highlight.js/lib/languages/sql"),
	swift: () => import("highlight.js/lib/languages/swift"),
	typescript: () => import("highlight.js/lib/languages/typescript"),
	xml: () => import("highlight.js/lib/languages/xml"),
	yaml: () => import("highlight.js/lib/languages/yaml"),
};

const ALIASES: Record<string, string> = {
	sh: "bash",
	shell: "bash",
	zsh: "bash",
	console: "bash",
	"c++": "cpp",
	cs: "csharp",
	golang: "go",
	toml: "ini",
	js: "javascript",
	jsx: "javascript",
	mjs: "javascript",
	cjs: "javascript",
	jsonc: "json",
	json5: "json",
	kt: "kotlin",
	md: "markdown",
	py: "python",
	rb: "ruby",
	rs: "rust",
	ts: "typescript",
	tsx: "typescript",
	html: "xml",
	svg: "xml",
	yml: "yaml",
	patch: "diff",
	docker: "dockerfile",
	make: "makefile",
};

let core: HLJSApi | null = null;
let corePromise: Promise<HLJSApi> | null = null;
const pending = new Map<string, Promise<boolean>>();

/** 把代码块语言名归一到已知的 highlight.js 语言；未知返回 null。 */
export function normalizeLanguage(
	lang: string | null | undefined,
): string | null {
	if (!lang) return null;
	const lower = lang.trim().toLowerCase();
	const name = ALIASES[lower] ?? lower;
	return name in LOADERS ? name : null;
}

function loadCore(): Promise<HLJSApi> {
	corePromise ??= import("highlight.js/lib/core").then((m) => {
		core = m.default;
		return core;
	});
	return corePromise;
}

/** 确保某语言已注册；返回是否可用。 */
export function ensureLanguage(lang: string): Promise<boolean> {
	const name = normalizeLanguage(lang);
	if (!name) return Promise.resolve(false);
	const loader = LOADERS[name];
	if (!loader) return Promise.resolve(false);
	let p = pending.get(name);
	if (!p) {
		p = Promise.all([loadCore(), loader()])
			.then(([hljs, mod]) => {
				if (!hljs.getLanguage(name)) hljs.registerLanguage(name, mod.default);
				return true;
			})
			.catch(() => false);
		pending.set(name, p);
	}
	return p;
}

/** 同步高亮：语言已加载时返回 HTML，否则 null。 */
export function highlightSync(
	code: string,
	lang: string | null | undefined,
): string | null {
	const name = normalizeLanguage(lang);
	if (!name || !core?.getLanguage(name)) return null;
	try {
		return core.highlight(code, { language: name, ignoreIllegals: true }).value;
	} catch {
		return null;
	}
}

/** 高亮 hook：首次渲染可能为 null，语言加载后触发重渲染。 */
export function useHighlighted(
	code: string,
	lang: string | null | undefined,
): string | null {
	const [ready, setReady] = useState(0);
	const name = normalizeLanguage(lang);
	useEffect(() => {
		if (!name || core?.getLanguage(name)) return;
		let alive = true;
		void ensureLanguage(name).then((ok) => {
			if (ok && alive) setReady((v) => v + 1);
		});
		return () => {
			alive = false;
		};
	}, [name]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready 用于在语言加载后重算
	return useMemo(() => highlightSync(code, name), [code, name, ready]);
}
