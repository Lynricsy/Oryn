/**
 * highlight.js 按需加载：核心与每种语言都是独立的懒加载分块，首屏不承担其体积。
 * 渲染端用 `useHighlighted` / `useHighlightedLines`：语言未加载时先返回 null（纯文本），加载完成后自动重渲染。
 * 按行高亮总是先高亮整段再切行，多行注释、字符串在折叠预览里也保持正确的着色状态。
 */
import type { HLJSApi, LanguageFn } from "highlight.js";
import { useEffect, useMemo, useState } from "react";

type LangLoader = () => Promise<{ default: LanguageFn }>;

const LOADERS: Record<string, LangLoader> = {
	armasm: () => import("highlight.js/lib/languages/armasm"),
	awk: () => import("highlight.js/lib/languages/awk"),
	bash: () => import("highlight.js/lib/languages/bash"),
	c: () => import("highlight.js/lib/languages/c"),
	clojure: () => import("highlight.js/lib/languages/clojure"),
	cmake: () => import("highlight.js/lib/languages/cmake"),
	cpp: () => import("highlight.js/lib/languages/cpp"),
	csharp: () => import("highlight.js/lib/languages/csharp"),
	css: () => import("highlight.js/lib/languages/css"),
	dart: () => import("highlight.js/lib/languages/dart"),
	diff: () => import("highlight.js/lib/languages/diff"),
	dockerfile: () => import("highlight.js/lib/languages/dockerfile"),
	elixir: () => import("highlight.js/lib/languages/elixir"),
	erlang: () => import("highlight.js/lib/languages/erlang"),
	fsharp: () => import("highlight.js/lib/languages/fsharp"),
	go: () => import("highlight.js/lib/languages/go"),
	graphql: () => import("highlight.js/lib/languages/graphql"),
	groovy: () => import("highlight.js/lib/languages/groovy"),
	haskell: () => import("highlight.js/lib/languages/haskell"),
	http: () => import("highlight.js/lib/languages/http"),
	ini: () => import("highlight.js/lib/languages/ini"),
	java: () => import("highlight.js/lib/languages/java"),
	javascript: () => import("highlight.js/lib/languages/javascript"),
	json: () => import("highlight.js/lib/languages/json"),
	julia: () => import("highlight.js/lib/languages/julia"),
	kotlin: () => import("highlight.js/lib/languages/kotlin"),
	latex: () => import("highlight.js/lib/languages/latex"),
	less: () => import("highlight.js/lib/languages/less"),
	lisp: () => import("highlight.js/lib/languages/lisp"),
	llvm: () => import("highlight.js/lib/languages/llvm"),
	lua: () => import("highlight.js/lib/languages/lua"),
	makefile: () => import("highlight.js/lib/languages/makefile"),
	markdown: () => import("highlight.js/lib/languages/markdown"),
	nginx: () => import("highlight.js/lib/languages/nginx"),
	nix: () => import("highlight.js/lib/languages/nix"),
	objectivec: () => import("highlight.js/lib/languages/objectivec"),
	ocaml: () => import("highlight.js/lib/languages/ocaml"),
	perl: () => import("highlight.js/lib/languages/perl"),
	pgsql: () => import("highlight.js/lib/languages/pgsql"),
	php: () => import("highlight.js/lib/languages/php"),
	powershell: () => import("highlight.js/lib/languages/powershell"),
	properties: () => import("highlight.js/lib/languages/properties"),
	protobuf: () => import("highlight.js/lib/languages/protobuf"),
	python: () => import("highlight.js/lib/languages/python"),
	r: () => import("highlight.js/lib/languages/r"),
	ruby: () => import("highlight.js/lib/languages/ruby"),
	rust: () => import("highlight.js/lib/languages/rust"),
	scala: () => import("highlight.js/lib/languages/scala"),
	scheme: () => import("highlight.js/lib/languages/scheme"),
	scss: () => import("highlight.js/lib/languages/scss"),
	shell: () => import("highlight.js/lib/languages/shell"),
	sql: () => import("highlight.js/lib/languages/sql"),
	swift: () => import("highlight.js/lib/languages/swift"),
	typescript: () => import("highlight.js/lib/languages/typescript"),
	vim: () => import("highlight.js/lib/languages/vim"),
	wasm: () => import("highlight.js/lib/languages/wasm"),
	x86asm: () => import("highlight.js/lib/languages/x86asm"),
	xml: () => import("highlight.js/lib/languages/xml"),
	yaml: () => import("highlight.js/lib/languages/yaml"),
};

const ALIASES: Record<string, string> = {
	sh: "bash",
	shell: "bash",
	zsh: "bash",
	fish: "bash",
	ksh: "bash",
	// `console` / `shell-session`：带 `$ ` 提示符的终端记录（hljs 的 shell 语言），与脚本区分
	console: "shell",
	"shell-session": "shell",
	shellsession: "shell",
	"c++": "cpp",
	cc: "cpp",
	cxx: "cpp",
	hpp: "cpp",
	hh: "cpp",
	h: "c",
	cs: "csharp",
	golang: "go",
	toml: "ini",
	cfg: "ini",
	conf: "ini",
	editorconfig: "ini",
	env: "properties",
	dotenv: "properties",
	js: "javascript",
	jsx: "javascript",
	mjs: "javascript",
	cjs: "javascript",
	node: "javascript",
	jsonc: "json",
	json5: "json",
	jsonl: "json",
	ndjson: "json",
	kt: "kotlin",
	kts: "kotlin",
	md: "markdown",
	mdx: "markdown",
	py: "python",
	py3: "python",
	python3: "python",
	ipython: "python",
	rb: "ruby",
	rs: "rust",
	ts: "typescript",
	tsx: "typescript",
	mts: "typescript",
	cts: "typescript",
	html: "xml",
	htm: "xml",
	xhtml: "xml",
	svg: "xml",
	vue: "xml",
	svelte: "xml",
	plist: "xml",
	yml: "yaml",
	patch: "diff",
	udiff: "diff",
	docker: "dockerfile",
	containerfile: "dockerfile",
	make: "makefile",
	mk: "makefile",
	ex: "elixir",
	exs: "elixir",
	erl: "erlang",
	fs: "fsharp",
	"f#": "fsharp",
	gql: "graphql",
	gradle: "groovy",
	hs: "haskell",
	jl: "julia",
	tex: "latex",
	ll: "llvm",
	clj: "clojure",
	cljs: "clojure",
	edn: "clojure",
	el: "lisp",
	elisp: "lisp",
	scm: "scheme",
	ml: "ocaml",
	pl: "perl",
	pm: "perl",
	postgres: "pgsql",
	postgresql: "pgsql",
	ps1: "powershell",
	pwsh: "powershell",
	ps: "powershell",
	proto: "protobuf",
	objc: "objectivec",
	"objective-c": "objectivec",
	mm: "objectivec",
	asm: "x86asm",
	nasm: "x86asm",
	x86: "x86asm",
	x64: "x86asm",
	arm: "armasm",
	aarch64: "armasm",
	arm64: "armasm",
	wat: "wasm",
	vimscript: "vim",
	viml: "vim",
};

/** 超过这个长度的文本不高亮：highlight.js 在主线程上同步运行，超长输出只按纯文本显示。 */
const MAX_HIGHLIGHT_CHARS = 200_000;

let core: HLJSApi | null = null;
let corePromise: Promise<HLJSApi> | null = null;
const pending = new Map<string, Promise<boolean>>();

/** 把代码块语言名归一到已知的 highlight.js 语言；未知返回 null。 */
export function normalizeLanguage(
	lang: string | null | undefined,
): string | null {
	if (!lang) return null;
	const lower = lang.trim().toLowerCase();
	const name = (Object.hasOwn(ALIASES, lower) && ALIASES[lower]) || lower;
	return Object.hasOwn(LOADERS, name) ? name : null;
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
	if (!name || !core?.getLanguage(name) || code.length > MAX_HIGHLIGHT_CHARS)
		return null;
	try {
		return core.highlight(code, { language: name, ignoreIllegals: true }).value;
	} catch {
		return null;
	}
}

const SPLIT_RE = /<span[^>]*>|<\/span>|\n/g;

/**
 * 把 highlight.js 的 HTML 切成逐行的 HTML：跨行的 `<span>` 在行尾闭合、下一行开头重新打开，
 * 每行都是标签配平的片段，可以单独放进一行 DOM。highlight.js 只输出 `<span class=…>` 与转义文本。
 */
export function splitHighlightedLines(html: string): string[] {
	const lines: string[] = [];
	const open: string[] = [];
	let line = "";
	let last = 0;
	SPLIT_RE.lastIndex = 0;
	for (let m = SPLIT_RE.exec(html); m; m = SPLIT_RE.exec(html)) {
		line += html.slice(last, m.index);
		last = SPLIT_RE.lastIndex;
		const tok = m[0];
		if (tok === "\n") {
			lines.push(line + "</span>".repeat(open.length));
			line = open.join("");
		} else {
			if (tok === "</span>") open.pop();
			else open.push(tok);
			line += tok;
		}
	}
	lines.push(line + html.slice(last));
	return lines;
}

/**
 * 确保这些语言已加载；返回一个在加载完成时变化的版本号，作为 `useMemo` 依赖让高亮在加载后重算。
 * 未知语言与 null 忽略。
 */
export function useLanguagesReady(
	langs: readonly (string | null | undefined)[],
): number {
	const [ready, setReady] = useState(0);
	const key = [
		...new Set(
			langs.map(normalizeLanguage).filter((n): n is string => n !== null),
		),
	]
		.sort()
		.join(",");
	useEffect(() => {
		const missing = key
			? key.split(",").filter((n) => !core?.getLanguage(n))
			: [];
		if (missing.length === 0) return;
		let alive = true;
		void Promise.all(missing.map(ensureLanguage)).then((oks) => {
			if (alive && oks.some(Boolean)) setReady((v) => v + 1);
		});
		return () => {
			alive = false;
		};
	}, [key]);
	return ready;
}

/** 高亮 hook：首次渲染可能为 null，语言加载后触发重渲染。 */
export function useHighlighted(
	code: string,
	lang: string | null | undefined,
): string | null {
	const name = normalizeLanguage(lang);
	const ready = useLanguagesReady([name]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready 用于在语言加载后重算
	return useMemo(() => highlightSync(code, name), [code, name, ready]);
}

/** 整段高亮后按行切开（行数与 `code.split("\n")` 相同）；语言未知、未加载或文本过长时为 null。 */
export function highlightLinesSync(
	code: string,
	lang: string | null | undefined,
): string[] | null {
	const html = highlightSync(code, lang);
	return html === null ? null : splitHighlightedLines(html);
}

/** 逐行高亮 hook：见 `highlightLinesSync`；语言加载后触发重渲染。 */
export function useHighlightedLines(
	code: string,
	lang: string | null | undefined,
): string[] | null {
	const name = normalizeLanguage(lang);
	const ready = useLanguagesReady([name]);
	// biome-ignore lint/correctness/useExhaustiveDependencies: ready 用于在语言加载后重算
	return useMemo(() => highlightLinesSync(code, name), [code, name, ready]);
}
