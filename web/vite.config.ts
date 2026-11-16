/// <reference types="vitest/config" />
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defaultAllowedOrigins, defineConfig, type Plugin } from "vite";

// 配置文件在 Node 里运行；项目不装 Node 类型，只声明这里用到的环境变量
declare const process: { env: Record<string, string | undefined> };

// pdf.js 运行时按需拉取的静态资源目录（CMap、标准字体、图像解码 wasm、ICC 配置文件），
// 预览组件以同源路径 `/pdfjs/<dir>/` 传给 getDocument
const PDFJS_ROOT = fileURLToPath(
	new URL("./node_modules/pdfjs-dist/", import.meta.url),
);
const PDFJS_DIRS = ["cmaps", "standard_fonts", "wasm", "iccs"].filter((d) =>
	existsSync(`${PDFJS_ROOT}${d}`),
);
// quickjs-eval 只服务于 PDF 内嵌脚本（enableScripting），预览从不启用，不随产物发布
const PDFJS_SKIP = /^quickjs-eval/;
const PDFJS_MIME: Record<string, string> = {
	js: "text/javascript; charset=utf-8",
	wasm: "application/wasm",
	ttf: "font/ttf",
};

// 开发时由中间件直接读 node_modules 提供 `/pdfjs/<dir>/<file>`；构建时把这些文件原样作为资源产出
function pdfjsAssets(): Plugin {
	return {
		name: "oryn-pdfjs-assets",
		configureServer(server) {
			server.middlewares.use((req, res, next) => {
				const m = /^\/pdfjs\/([a-z_]+)\/([^/?#]+)(?:[?#]|$)/.exec(
					req.url ?? "",
				);
				if (!m?.[1] || !m[2] || !PDFJS_DIRS.includes(m[1])) return next();
				let file: string;
				try {
					file = decodeURIComponent(m[2]);
				} catch {
					return next();
				}
				// 文件名只允许单层普通名字，拒绝 `..`、隐藏文件与编码后的分隔符（防目录穿越）
				if (
					file.startsWith(".") ||
					/[/\\\0]/.test(file) ||
					PDFJS_SKIP.test(file)
				) {
					return next();
				}
				const full = `${PDFJS_ROOT}${m[1]}/${file}`;
				if (!existsSync(full) || !statSync(full).isFile()) return next();
				const ext = file.slice(file.lastIndexOf(".") + 1).toLowerCase();
				res.setHeader(
					"Content-Type",
					PDFJS_MIME[ext] ?? "application/octet-stream",
				);
				res.end(readFileSync(full));
			});
		},
		generateBundle() {
			for (const dir of PDFJS_DIRS) {
				for (const file of readdirSync(`${PDFJS_ROOT}${dir}`)) {
					const full = `${PDFJS_ROOT}${dir}/${file}`;
					if (PDFJS_SKIP.test(file) || !statSync(full).isFile()) continue;
					this.emitFile({
						type: "asset",
						fileName: `pdfjs/${dir}/${file}`,
						source: readFileSync(full),
					});
				}
			}
		},
	};
}

// 开发时把 API、健康检查、portal 授权端点（`/portal/auth`）、公开文件（`/f/…`）与分享的 Markdown 导出（`/s/<slug>.md`）
// 转发到本机 orynd（dev/orynd.yaml 监听 127.0.0.1:7600）；分享页 `/s/<slug>` 本身由 vite 提供
const orynd = "http://127.0.0.1:7600";

// `--mode launcher`（`bun run build:launcher`）只构建桌面壳内置的连接页（launcher.html → dist-launcher/），其余不变
export default defineConfig(({ mode }) => ({
	plugins: [
		react(),
		tailwindcss(),
		// 桌面壳连接页不含文件预览，不带 pdf.js 资源
		...(mode === "launcher" ? [] : [pdfjsAssets()]),
	],
	build:
		mode === "launcher"
			? {
					outDir: "dist-launcher",
					emptyOutDir: true,
					rolldownOptions: { input: "launcher.html" },
				}
			: {
					// 超过 500KB 的只有按需加载的分块：mermaid 子图（elk、cytoscape 等）与文件预览的解码器，
					// 最大的是内联了 libheif WASM 的 HEIC 解码（约 3.2MB），只在打开 .heic 时加载，不在首屏
					chunkSizeWarningLimit: 3300,
				},
	server: {
		// 壳工程的 Rust 构建产物（src-tauri/target）不需要 vite 监视
		watch: { ignored: ["**/src-tauri/**"] },
		// mock 模式（`dev:mock`）的 artifact 帧是沙箱里的 blob 页面（不透明源，请求带 `Origin: null`），
		// 要从开发服务器加载 `sandbox/runtime/main.ts`：额外放行 `null`。
		// 真后端模式下帧挂在 `/_af/`（经下面的代理到 orynd），页面运行时的 `POST _oryn/v1` 带 `Origin: null`
		// 且要预检；vite 的 CORS 中间件排在代理之前，会自己回一个不带 ACAO 的 204 把预检截住。对 `null`
		// 返回 false 让 CORS 中间件整个跳过：代理路径由 orynd 回自己的 CORS 头，其余路径照旧没有 CORS 头。
		cors: {
			origin: (origin, cb) => {
				const allowed = process.env.VITE_MOCK
					? [defaultAllowedOrigins, "null"]
					: origin === "null"
						? false
						: defaultAllowedOrigins;
				// vite 的类型把 err 写成必填；cors 包按 Node 回调约定接受 null
				cb(null as unknown as Error, allowed);
			},
		},
		proxy: {
			"/api": { target: orynd, ws: true, changeOrigin: true },
			"/healthz": { target: orynd, changeOrigin: true },
			"/portal": { target: orynd, changeOrigin: true },
			"/f/": { target: orynd, changeOrigin: true },
			"^/s/[0-9a-f]{32}\\.md$": { target: orynd, changeOrigin: true },
			// 未配置 portal_domain 时 artifact 的帧与发布页挂在主域路径下（M14）
			"/_af/": { target: orynd, changeOrigin: true },
			"/p/": { target: orynd, changeOrigin: true },
		},
	},
	test: {
		environment: "jsdom",
		globals: true,
		setupFiles: ["./src/test/setup.ts"],
	},
}));
