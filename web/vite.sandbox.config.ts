import { defineConfig } from "vite";

// 沙箱资源：运行在 artifact 页面（不可信、不透明源）里的脚本，由 orynd 内联或按固定名字提供，所以文件名不带哈希。
// 在主构建之后执行，只往 dist/sandbox/ 里加文件，不清空 dist。库模式一次只能输出一个 IIFE，所以每个 IIFE
// 入口单独构建一次，用 --mode 选：缺省（production）是页面运行时，`--mode overview` 是画布的只读概览页，
// `--mode beacon` 是发布页的访客心跳，`--mode probe` 是设计检查的探针（M29）。
const iife: Record<string, { entry: string; name: string; file: string }> = {
	production: {
		entry: "sandbox/runtime/main.ts",
		name: "orynRuntime",
		file: "sandbox/runtime.js",
	},
	overview: {
		entry: "sandbox/overview/main.ts",
		name: "orynOverview",
		file: "sandbox/overview.js",
	},
	beacon: {
		entry: "sandbox/beacon/main.ts",
		name: "orynBeacon",
		file: "sandbox/beacon.js",
	},
	probe: {
		entry: "sandbox/probe/main.ts",
		name: "orynProbe",
		file: "sandbox/probe.js",
	},
};

// 类型化 artifact 的查看器：ES module，自带 React，输出 `sandbox/viewers/<名字>.{js,css}`（orynd 只认这四个名字）。
// 每个查看器单独构建一次（`--mode viewer-document` / `--mode viewer-slides`）：多入口同构建会把 React
// 拆进带哈希的公共分块，服务端不提供它。
const viewers: Record<string, string> = {
	"viewer-document": "document",
	"viewer-slides": "slides",
};

export default defineConfig(({ mode }) => {
	const viewer = viewers[mode];
	if (viewer)
		return {
			publicDir: false,
			// 库模式不替换 process.env；React 要按生产版本打包
			define: { "process.env.NODE_ENV": JSON.stringify("production") },
			build: {
				outDir: "dist",
				emptyOutDir: false,
				copyPublicDir: false,
				// ES 库模式缺省不压缩空白；查看器是直接给页面加载的成品，且必须是单个文件
				rolldownOptions: { output: { minify: true, codeSplitting: false } },
				lib: {
					entry: `sandbox/viewers/${viewer}/main.tsx`,
					formats: ["es"],
					fileName: () => `sandbox/viewers/${viewer}.js`,
					cssFileName: `sandbox/viewers/${viewer}`,
				},
			},
		};
	const target = iife[mode];
	if (!target) throw new Error(`未知的沙箱构建入口：${mode}`);
	return {
		publicDir: false,
		build: {
			outDir: "dist",
			emptyOutDir: false,
			copyPublicDir: false,
			lib: {
				// 相对路径按 vite 的 root（web/）解析
				entry: target.entry,
				formats: ["iife"],
				name: target.name,
				fileName: () => target.file,
			},
		},
	};
});
