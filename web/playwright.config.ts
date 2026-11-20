/**
 * UI 排版审计（e2e/ui-audit）：mock 模式的 vite 开发服务器 + 本机 Chromium。
 * 视口 1920×1080、1366×768、390×844（触屏）× 明暗两套主题，共 6 个 project。
 *
 *   npx playwright test e2e/ui-audit                      # 断言 + 截图（after）
 *   UI_AUDIT_PHASE=before UI_AUDIT_WEB=<基线 worktree>/web npx playwright test e2e/ui-audit
 *
 * 截图写到 `docs/ui-audit/shots/<界面 id>/<project>-<phase>.png`（不入库）。
 */
import { existsSync } from "node:fs";
import { defineConfig, devices } from "@playwright/test";

// e2e/ui-audit/netns.ts 开着时在只有回环的网络命名空间里起浏览器，避开 Docker veth 变化掐断请求
const NETNS_CHROMIUM = "/run/oryn-ui-audit/chromium.sh";

const PHASE = process.env.UI_AUDIT_PHASE === "before" ? "before" : "after";
// before 与 after 各用一个端口，两个服务器可以同时开着，reuseExistingServer 不会接错
const PORT = PHASE === "before" ? 5294 : 5293;

const VIEWPORTS = [
	{ name: "d1920", viewport: { width: 1920, height: 1080 }, mobile: false },
	{ name: "d1366", viewport: { width: 1366, height: 768 }, mobile: false },
	{ name: "m390", viewport: { width: 390, height: 844 }, mobile: true },
] as const;
const SCHEMES = ["light", "dark"] as const;

// 主进程定下运行 id 写进环境变量，worker 进程重新加载配置时继承同一个值
process.env.UI_AUDIT_RUN ??= `${process.env.UI_AUDIT_PHASE ?? "after"}-${Date.now()}`;
const RUN = process.env.UI_AUDIT_RUN;

export default defineConfig({
	testDir: "e2e",
	testMatch: "**/*.e2e.ts",
	fullyParallel: true,
	forbidOnly: true,
	retries: 0,
	workers: Number(process.env.UI_AUDIT_WORKERS ?? 8),
	// SwiftShader 软件合成很吃 CPU，几路审计并行时单个用例常要几十秒
	timeout: 150_000,
	expect: { timeout: 5_000 },
	// 每次运行一个产物目录：几路审计并行时，运行开始清空 outputDir 不会删掉别人还在写的 trace
	outputDir: `test-results/${RUN}`,
	reporter: [
		["list"],
		["json", { outputFile: `test-results/${RUN}/ui-audit.json` }],
		// 按用例最终状态（含 fixture 清理）覆写结果 JSON：清理超时等不会留下假绿
		["./e2e/ui-audit/result-reporter.ts"],
	],
	use: {
		baseURL: `http://127.0.0.1:${PORT}`,
		locale: "zh-CN",
		timezoneId: "Asia/Shanghai",
		trace: "retain-on-failure",
		launchOptions: {
			executablePath:
				process.env.CHROMIUM ??
				(existsSync(NETNS_CHROMIUM) ? NETNS_CHROMIUM : "/usr/bin/chromium"),
			// 软件合成器下模拟 DPR 时 backdrop-filter 会部分失效（.agent-logs 0312），改走 SwiftShader
			args: ["--use-gl=angle", "--use-angle=swiftshader"],
		},
	},
	projects: VIEWPORTS.flatMap((v) =>
		SCHEMES.map((scheme) => ({
			name: `${v.name}-${scheme}`,
			use: {
				...devices["Desktop Chrome"],
				viewport: v.viewport,
				deviceScaleFactor: v.mobile ? 2 : 1,
				isMobile: v.mobile,
				hasTouch: v.mobile,
				colorScheme: scheme,
			},
		})),
	),
	webServer: {
		command: `bunx vite --port ${PORT} --strictPort --host 127.0.0.1`,
		cwd: process.env.UI_AUDIT_WEB ?? ".",
		env: { VITE_MOCK: "1" },
		url: `http://127.0.0.1:${PORT}/`,
		reuseExistingServer: true,
		timeout: 60_000,
	},
});
