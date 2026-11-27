// 设计画布引擎插件（M29；M34 加方案主题）。工作副本布局见 docs/plan-m29.md C2、C3 与 docs/plan-m34.md C2：
//   <work>/src/frames/<id>.tsx   一帧一个文件，默认导出组件；可选 export const frame = {name, width, height, device}
//   <work>/src/frames/_*.tsx     `_` 开头是共享外壳（_layout.tsx 包住每一帧），不是帧
//   <work>/src/variants.json     可选的方案清单（解析见 variants.mjs）：决定每帧用哪套方案主题
//   <work>/src/themes/<名>.css   方案主题：一条 :root[data-oryn-theme="<名>"] {…} 规则，在 theme.css 之后导入
//   <work>/.oryn-build/          引擎生成的入口（vite root），不同步
//   <work>/dist/                 构建输出：<id>.html、assets/**、frames.json
//
// 每帧入口 = .oryn-build/<id>.html（dev 不用）+ .oryn-build/<id>.entry.tsx（引用引擎 entry.tsx 的 mount）。
// dev：GET /frames/<id> 按需生成入口 HTML（不落盘）；帧文件增删或 _layout 增删时重写入口并整页重载。

import fs from "node:fs";
import path from "node:path";
import { parseAst, transformWithEsbuild } from "vite";
import { parseVariants, VARIANT_ID } from "./variants.mjs";

/** 帧 id 与画布 page 的 source 同一规则（C1）。 */
const FRAME_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;
const DEVICES = new Set(["none", "phone", "tablet", "browser"]);
const DEFAULT_WIDTH = 1280;
const DEFAULT_HEIGHT = 800;
const MIN_SIZE = 64;
const MAX_SIZE = 8000;
const MAX_NAME_RUNES = 120;
/** styles.css 里由 oryn-design-css 替换的占位注释。 */
const CSS_PLACEHOLDER = "/* oryn:design-css */";

/**
 * @typedef {{ name: string, width: number, height: number, device: string, links: string[] }} FrameMeta
 * @typedef {{ work: string, engine: string }} Dirs
 */

function framesDir(work) {
	return path.join(work, "src", "frames");
}

function buildDir(work) {
	return path.join(work, ".oryn-build");
}

function layoutFile(work) {
	return path.join(framesDir(work), "_layout.tsx");
}

function themesDir(work) {
	return path.join(work, "src", "themes");
}

function variantsFile(work) {
	return path.join(work, "src", "variants.json");
}

/** src/themes 下合规的方案主题名（文件名去掉 .css，字典序）；不合规的文件名不导入。 */
function scanThemes(work) {
	let names = [];
	try {
		names = fs.readdirSync(themesDir(work), { withFileTypes: true });
	} catch {
		return [];
	}
	return names
		.filter((d) => d.isFile() && d.name.endsWith(".css"))
		.map((d) => d.name.slice(0, -".css".length))
		.filter((name) => VARIANT_ID.test(name))
		.sort();
}

/**
 * 读方案清单，返回「帧 → 主题」；没有清单时为空。strict（build）时清单不合法即抛错，否则（dev）警告后按空清单。
 * 主题文件里没有对应选择器时只警告：变量缺失由探针的 token-undefined 兜底。
 * @returns {Record<string, string>}
 */
function frameThemes(work, ids, warn, strict) {
	const themes = scanThemes(work);
	for (const name of themes) {
		const css = fs.readFileSync(path.join(themesDir(work), `${name}.css`), "utf8");
		if (!css.includes(`[data-oryn-theme="${name}"]`))
			warn(`src/themes/${name}.css: 应只写一条 :root[data-oryn-theme="${name}"] {…} 规则`);
	}
	let raw;
	try {
		raw = fs.readFileSync(variantsFile(work), "utf8");
	} catch {
		return {};
	}
	try {
		return parseVariants(raw, ids, themes).frameTheme;
	} catch (e) {
		const message = `src/variants.json 不合法：${e instanceof Error ? e.message : String(e)}`;
		if (strict) throw new Error(message);
		warn(message);
		return {};
	}
}

/** dev 下重写全部帧入口：方案清单不合法时警告并按空清单。 */
function writeDevEntries(work) {
	const ids = scanFrames(work).ids;
	writeEntries(work, ids, null, frameThemes(work, ids, (m) => console.warn(`[oryn-design] ${m}`), false));
}

/** src/frames 下的帧 id（字典序）与不合规的文件名。 */
function scanFrames(work) {
	/** @type {string[]} */
	const ids = [];
	/** @type {string[]} */
	const invalid = [];
	let names = [];
	try {
		names = fs.readdirSync(framesDir(work), { withFileTypes: true });
	} catch {
		return { ids, invalid };
	}
	for (const d of names) {
		if (!d.isFile() || !d.name.endsWith(".tsx") || d.name.startsWith("_"))
			continue;
		const id = d.name.slice(0, -".tsx".length);
		(FRAME_ID.test(id) ? ids : invalid).push(id);
	}
	ids.sort();
	invalid.sort();
	return { ids, invalid };
}

/** 极简 AST 遍历：对每个带 type 的节点调用 visit。 */
function walk(node, visit) {
	if (Array.isArray(node)) {
		for (const n of node) walk(n, visit);
		return;
	}
	if (node === null || typeof node !== "object") return;
	if (typeof node.type === "string") visit(node);
	for (const key of Object.keys(node)) {
		if (key === "start" || key === "end" || key === "type") continue;
		const v = node[key];
		if (v !== null && typeof v === "object") walk(v, visit);
	}
}

/** 字面量字符串（含无插值模板字符串）的值；不是则为 undefined。 */
function staticString(node) {
	if (!node) return undefined;
	if (node.type === "Literal" && typeof node.value === "string")
		return node.value;
	if (
		node.type === "TemplateLiteral" &&
		node.expressions.length === 0 &&
		node.quasis.length === 1
	)
		return node.quasis[0].value.cooked ?? undefined;
	if (node.type === "JSXExpressionContainer") return staticString(node.expression);
	return undefined;
}

function staticNumber(node) {
	if (node?.type === "Literal" && typeof node.value === "number")
		return node.value;
	return undefined;
}

function propKey(prop) {
	if (prop.type !== "Property" || prop.computed) return undefined;
	if (prop.key.type === "Identifier") return prop.key.name;
	if (prop.key.type === "Literal") return String(prop.key.value);
	return undefined;
}

/**
 * 静态读帧文件：`export const frame = {…}` 里的字面量与 `<Link to="字面量">` 的目标。
 * 不执行设计代码；取不到或不合法的字段回落到缺省值并记入 warnings。
 * @returns {Promise<{ meta: FrameMeta, warnings: string[] }>}
 */
async function readFrame(work, id) {
	const file = path.join(framesDir(work), `${id}.tsx`);
	const rel = `src/frames/${id}.tsx`;
	const code = fs.readFileSync(file, "utf8");
	const js = await transformWithEsbuild(code, file, {
		loader: "tsx",
		jsx: "preserve",
		sourcemap: false,
	});
	const ast = parseAst(js.code, { jsx: true });
	/** @type {FrameMeta} */
	const meta = {
		name: id,
		width: DEFAULT_WIDTH,
		height: DEFAULT_HEIGHT,
		device: "none",
		links: [],
	};
	const warnings = [];
	/** @type {Record<string, any>} */
	let decl;
	walk(ast.body, (n) => {
		if (
			n.type === "ExportNamedDeclaration" &&
			n.declaration?.type === "VariableDeclaration"
		) {
			for (const d of n.declaration.declarations) {
				if (d.id.type === "Identifier" && d.id.name === "frame") decl = d.init;
			}
		}
		if (
			n.type === "JSXOpeningElement" &&
			n.name.type === "JSXIdentifier" &&
			n.name.name === "Link"
		) {
			for (const a of n.attributes) {
				if (a.type !== "JSXAttribute" || a.name.name !== "to") continue;
				const to = staticString(a.value);
				if (to !== undefined && !meta.links.includes(to)) meta.links.push(to);
			}
		}
	});
	if (decl !== undefined) {
		if (decl?.type !== "ObjectExpression") {
			warnings.push(`${rel}: export const frame 应是对象字面量，已忽略`);
		} else {
			for (const prop of decl.properties) {
				const key = propKey(prop);
				const value = prop.value;
				if (key === "name") {
					const s = staticString(value)?.trim();
					if (s && [...s].length <= MAX_NAME_RUNES) meta.name = s;
					else warnings.push(`${rel}: frame.name 应是 1–${MAX_NAME_RUNES} 字的字符串字面量`);
				} else if (key === "width" || key === "height") {
					const v = staticNumber(value);
					if (v !== undefined && v >= MIN_SIZE && v <= MAX_SIZE)
						meta[key] = Math.round(v);
					else warnings.push(`${rel}: frame.${key} 应是 ${MIN_SIZE}–${MAX_SIZE} 的数字字面量`);
				} else if (key === "device") {
					const s = staticString(value);
					if (s !== undefined && DEVICES.has(s)) meta.device = s;
					else warnings.push(`${rel}: frame.device 应是 none / phone / tablet / browser`);
				}
			}
		}
	}
	return { meta, warnings };
}

/** 读全部帧；links 只留存在的、不是自身的帧。 */
async function readFrames(work, ids, warn) {
	/** @type {Map<string, FrameMeta>} */
	const out = new Map();
	for (const id of ids) {
		const { meta, warnings } = await readFrame(work, id);
		for (const w of warnings) warn(w);
		out.set(id, meta);
	}
	for (const [id, meta] of out) {
		for (const to of meta.links) {
			if (!out.has(to)) warn(`src/frames/${id}.tsx: <Link to="${to}"> 指向不存在的帧`);
		}
		meta.links = meta.links.filter((to) => to !== id && out.has(to));
	}
	return out;
}

function escapeHtml(s) {
	return s
		.replaceAll("&", "&amp;")
		.replaceAll("<", "&lt;")
		.replaceAll(">", "&gt;")
		.replaceAll('"', "&quot;");
}

/** 帧入口 HTML。dev 版另带 #oryn-design（{frame, links}），引擎入口据此配置页面运行时。 */
function frameHtml(id, name, design) {
	const data = design
		? `\n<script type="application/json" id="oryn-design">${JSON.stringify(design).replaceAll("<", "\\u003c")}</script>`
		: "";
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>${escapeHtml(name)}</title>${data}
</head>
<body>
<div id="root"></div>
<script type="module" src="/${id}.entry.tsx"></script>
</body>
</html>
`;
}

/** 帧入口模块：只依赖帧 id、_layout 是否存在与帧的方案主题；dev 下随帧或外壳热替换重渲染。 */
function frameEntry(id, hasLayout, theme) {
	const frame = JSON.stringify(`../src/frames/${id}.tsx`);
	const layout = JSON.stringify("../src/frames/_layout.tsx");
	const lines = [
		`import { mount } from "oryn:engine";`,
		`import * as frame from ${frame};`,
	];
	if (hasLayout) lines.push(`import * as layout from ${layout};`);
	const opts = [`id: ${JSON.stringify(id)}`, "frame"];
	if (hasLayout) opts.push("layout");
	if (theme) opts.push(`theme: ${JSON.stringify(theme)}`);
	lines.push(
		`const view = mount({ ${opts.join(", ")} });`,
		`if (import.meta.hot) {`,
		`\timport.meta.hot.accept(${frame}, (m) => m && view.update(m));`,
	);
	if (hasLayout)
		lines.push(`\timport.meta.hot.accept(${layout}, (m) => m && view.update(undefined, m));`);
	lines.push(`}`, ``);
	return lines.join("\n");
}

/** 内容不同才写，免得触发无谓的 HMR。 */
function writeIfChanged(file, content) {
	try {
		if (fs.readFileSync(file, "utf8") === content) return;
	} catch {}
	fs.writeFileSync(file, content);
}

/**
 * 写每帧入口模块（build 另写入口 HTML），删掉已不存在的帧的入口。themes 是「帧 → 方案主题」。
 * dev 不碰 .html：封存时的 vite build 可能与 dev server 同时在跑，它的入口 HTML 只由 build 自己清理。
 */
function writeEntries(work, ids, html, themes) {
	const dir = buildDir(work);
	fs.mkdirSync(dir, { recursive: true });
	const keep = new Set();
	const hasLayout = fs.existsSync(layoutFile(work));
	for (const id of ids) {
		keep.add(`${id}.entry.tsx`);
		writeIfChanged(path.join(dir, `${id}.entry.tsx`), frameEntry(id, hasLayout, themes[id]));
		if (html) {
			keep.add(`${id}.html`);
			writeIfChanged(path.join(dir, `${id}.html`), html.get(id));
		}
	}
	for (const d of fs.readdirSync(dir, { withFileTypes: true })) {
		if (!d.isFile() || keep.has(d.name)) continue;
		if (d.name.endsWith(".entry.tsx") || (html && d.name.endsWith(".html")))
			fs.rmSync(path.join(dir, d.name), { force: true });
	}
}

function noFramesError() {
	return new Error("src/frames 下没有帧：每帧一个 src/frames/<id>.tsx 文件");
}

function invalidFramesMessage(invalid) {
	return `帧文件名不合规（应匹配 ${FRAME_ID}，共享外壳以 _ 开头）：${invalid.map((id) => `src/frames/${id}.tsx`).join("、")}`;
}

/**
 * 帧插件：build 生成多入口与 dist/frames.json；dev 提供 /frames/<id>、/frames/runtime.js、/frames/probe.js。
 * @param {{ work: string, sandbox: string }} dirs sandbox 是页面运行时与探针脚本所在目录（可为空串）
 * @returns {import("vite").Plugin}
 */
export function orynFrames({ work, sandbox }) {
	/** @type {Map<string, FrameMeta>} */
	let frames = new Map();
	return {
		name: "oryn-design-frames",
		async config(_, env) {
			if (env.command !== "build") {
				writeDevEntries(work);
				return;
			}
			const { ids, invalid } = scanFrames(work);
			if (invalid.length > 0) throw new Error(invalidFramesMessage(invalid));
			if (ids.length === 0) throw noFramesError();
			frames = await readFrames(work, ids, (m) => console.warn(`[oryn-design] ${m}`));
			const html = new Map(ids.map((id) => [id, frameHtml(id, frames.get(id).name, null)]));
			const themes = frameThemes(work, ids, (m) => console.warn(`[oryn-design] ${m}`), true);
			writeEntries(work, ids, html, themes);
			const dir = buildDir(work);
			return {
				build: {
					rollupOptions: {
						input: Object.fromEntries(ids.map((id) => [id, path.join(dir, `${id}.html`)])),
					},
				},
			};
		},
		generateBundle() {
			const list = [...frames].map(([id, m]) => ({
				id,
				name: m.name,
				width: m.width,
				height: m.height,
				device: m.device,
				links: m.links,
			}));
			this.emitFile({
				type: "asset",
				fileName: "frames.json",
				source: `${JSON.stringify(list, null, 2)}\n`,
			});
		},
		configureServer(server) {
			const src = path.join(work, "src");
			const frameDir = framesDir(work) + path.sep;
			server.watcher.add(src);
			const onFrameSetChange = (file) => {
				if (!file.startsWith(frameDir) || !file.endsWith(".tsx")) return;
				if (path.dirname(file) + path.sep !== frameDir) return;
				writeDevEntries(work);
				server.ws.send({ type: "full-reload" });
			};
			server.watcher.on("add", onFrameSetChange);
			server.watcher.on("unlink", onFrameSetChange);
			// 方案清单改动、主题文件增删会改变帧的方案主题：重写入口并整页重载（主题文件内容的改动由 Tailwind 热更）
			const variants = variantsFile(work);
			const themeDir = themesDir(work) + path.sep;
			const onVariantsChange = (file) => {
				if (file !== variants && !(path.dirname(file) + path.sep === themeDir && file.endsWith(".css")))
					return;
				writeDevEntries(work);
				server.ws.send({ type: "full-reload" });
			};
			server.watcher.on("add", onVariantsChange);
			server.watcher.on("unlink", onVariantsChange);
			server.watcher.on("change", (file) => {
				if (file === variants) onVariantsChange(file);
			});

			server.middlewares.use(async (req, res, next) => {
				if (req.method !== "GET" && req.method !== "HEAD") return next();
				const url = new URL(req.url ?? "/", "http://x");
				const p = url.pathname;
				try {
					if (p === "/") {
						const first = scanFrames(work).ids[0];
						if (first === undefined) {
							res.statusCode = 404;
							res.setHeader("Content-Type", "text/plain; charset=utf-8");
							res.end(noFramesError().message);
							return;
						}
						res.statusCode = 302;
						res.setHeader("Location", `/frames/${first}`);
						res.end();
						return;
					}
					if (p === "/frames/runtime.js" || p === "/frames/probe.js") {
						const file = sandbox ? path.join(sandbox, path.basename(p)) : "";
						if (!file || !fs.existsSync(file)) {
							res.statusCode = 404;
							res.end();
							return;
						}
						res.setHeader("Content-Type", "text/javascript; charset=utf-8");
						res.setHeader("Cache-Control", "no-cache");
						res.end(fs.readFileSync(file));
						return;
					}
					const m = /^\/frames\/([^/]+)$/.exec(p);
					if (!m) return next();
					const id = m[1];
					if (!FRAME_ID.test(id) || !fs.existsSync(path.join(framesDir(work), `${id}.tsx`))) {
						res.statusCode = 404;
						res.setHeader("Content-Type", "text/plain; charset=utf-8");
						res.end(`没有帧 src/frames/${id}.tsx`);
						return;
					}
					writeDevEntries(work);
					const ids = scanFrames(work).ids;
					// 帧正在编辑、一时解析不了时仍要能打开页面：标题与链接回落到缺省值
					let name = id;
					let links = [];
					try {
						const all = await readFrames(work, ids, () => {});
						name = all.get(id)?.name ?? id;
						links = all.get(id)?.links ?? [];
					} catch {}
					const html = await server.transformIndexHtml(
						`/frames/${id}`,
						frameHtml(id, name, { frame: id, links }),
						req.originalUrl,
					);
					res.statusCode = 200;
					res.setHeader("Content-Type", "text/html; charset=utf-8");
					res.setHeader("Cache-Control", "no-cache");
					res.end(html);
				} catch (e) {
					next(e);
				}
			});
		},
	};
}

/**
 * Tailwind 接入（探针 P2）：把引擎 styles.css 里的占位注释替换为
 * `@import "<work>/src/theme.css"; @import "<work>/src/themes/<名>.css"…; @source "<work>/src";`
 * （theme.css 不存在时不导入它；方案主题按文件名顺序导入，不在任何 layer 里，覆盖 @layer theme 的变量，见 M34 S0）。
 * 必须 enforce:"pre" 且在 plugins 里排在 @tailwindcss/vite 之前，否则自定义 token 与类都不生成。
 * @param {Dirs} dirs
 * @returns {import("vite").Plugin}
 */
export function orynDesignCss({ work, engine }) {
	const styles = path.join(engine, "styles.css");
	const theme = path.join(work, "src", "theme.css");
	const themeDir = themesDir(work) + path.sep;
	return {
		name: "oryn-design-css",
		enforce: "pre",
		transform(code, id) {
			if (id.split("?")[0] !== styles || !code.includes(CSS_PLACEHOLDER)) return;
			const lines = [];
			if (fs.existsSync(theme)) lines.push(`@import ${JSON.stringify(theme)};`);
			for (const name of scanThemes(work))
				lines.push(`@import ${JSON.stringify(path.join(themesDir(work), `${name}.css`))};`);
			lines.push(`@source ${JSON.stringify(path.join(work, "src"))};`);
			return { code: code.replace(CSS_PLACEHOLDER, lines.join("\n")), map: null };
		},
		configureServer(server) {
			// theme.css 或方案主题文件新建、删除时 styles.css 要重新替换占位
			const reload = (file) => {
				if (file !== theme && !(path.dirname(file) + path.sep === themeDir && file.endsWith(".css"))) return;
				for (const mod of server.moduleGraph.getModulesByFile(styles) ?? [])
					void server.reloadModule(mod);
			};
			server.watcher.on("add", reload);
			server.watcher.on("unlink", reload);
		},
	};
}

/**
 * 设计源码里的裸模块：先按 vite 的常规方式从引入者目录解析（设计级依赖在 <work>/src/node_modules），
 * 解析不到再从引擎目录解析，设计稿因此可直接用引擎自带的 lucide-react 等包。
 * @param {Dirs} dirs
 * @returns {import("vite").Plugin}
 */
export function orynDesignResolve({ work, engine }) {
	const src = path.join(work, "src") + path.sep;
	const anchor = path.join(engine, "entry.tsx");
	return {
		name: "oryn-design-resolve",
		enforce: "pre",
		async resolveId(source, importer, options) {
			if (!importer || !importer.startsWith(src)) return null;
			if (source.startsWith(".") || source.startsWith("/") || source.includes(":") || source.startsWith("\0"))
				return null;
			const own = await this.resolve(source, importer, { ...options, skipSelf: true });
			if (own) return own;
			return this.resolve(source, anchor, { ...options, skipSelf: true });
		},
	};
}
