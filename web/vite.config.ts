/// <reference types="vitest/config" />
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defaultAllowedOrigins, defineConfig } from "vite";

// 配置文件在 Node 里运行；项目不装 Node 类型，只声明这里用到的环境变量
declare const process: { env: Record<string, string | undefined> };

// 开发时把 API、健康检查、portal 授权端点（`/portal/auth`）、公开文件（`/f/…`）与分享的 Markdown 导出（`/s/<slug>.md`）
// 转发到本机 orynd（dev/orynd.yaml 监听 127.0.0.1:7600）；分享页 `/s/<slug>` 本身由 vite 提供
const orynd = "http://127.0.0.1:7600";

// `--mode launcher`（`bun run build:launcher`）只构建桌面壳内置的连接页（launcher.html → dist-launcher/），其余不变
export default defineConfig(({ mode }) => ({
	plugins: [react(), tailwindcss()],
	build:
		mode === "launcher"
			? {
					outDir: "dist-launcher",
					emptyOutDir: true,
					rolldownOptions: { input: "launcher.html" },
				}
			: {
					// 超过 500KB 的只有按需加载的 mermaid 子图分块（elk、cytoscape 等），不在首屏
					chunkSizeWarningLimit: 1600,
				},
	server: {
		// 壳工程的 Rust 构建产物（src-tauri/target）不需要 vite 监视
		watch: { ignored: ["**/src-tauri/**"] },
		// mock 模式（`dev:mock`）的 artifact 帧是沙箱里的 blob 页面（不透明源，请求带 `Origin: null`），
		// 要从开发服务器加载 `sandbox/runtime/main.ts`；只在 mock 模式下额外放行 `null`
		...(process.env.VITE_MOCK
			? { cors: { origin: [defaultAllowedOrigins, "null"] } }
			: {}),
		proxy: {
			"/api": { target: orynd, ws: true, changeOrigin: true },
			"/healthz": { target: orynd, changeOrigin: true },
			"/portal": { target: orynd, changeOrigin: true },
			"/f/": { target: orynd, changeOrigin: true },
			"^/s/[0-9a-f]{32}\\.md$": { target: orynd, changeOrigin: true },
		},
	},
	test: {
		environment: "jsdom",
		globals: true,
		setupFiles: ["./src/test/setup.ts"],
	},
}));
