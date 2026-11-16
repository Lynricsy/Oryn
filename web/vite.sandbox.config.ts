import { defineConfig } from "vite";

// 沙箱资源：运行在 artifact 页面（不可信、不透明源）里的脚本，由 orynd 内联或按固定名字提供，所以文件名不带哈希。
// 在主构建之后执行（`vite build -c vite.sandbox.config.ts`），只往 dist/sandbox/ 里加文件，不清空 dist。
export default defineConfig({
	publicDir: false,
	build: {
		outDir: "dist",
		emptyOutDir: false,
		copyPublicDir: false,
		lib: {
			// 相对路径按 vite 的 root（web/）解析
			entry: "sandbox/runtime/main.ts",
			formats: ["iife"],
			name: "orynRuntime",
			fileName: () => "sandbox/runtime.js",
		},
	},
});
