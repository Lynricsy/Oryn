/**
 * Playwright globalSetup：root 下每次运行把浏览器放进本次独占的、只有回环的网络命名空间（netns.ts）。
 *
 * 本机 Docker orb 的 veth 每几秒增删一次，Chromium 收到网卡变化就中止全部在途请求（net::ERR_NETWORK_CHANGED）；
 * vite 开发服务器逐个模块加载，页面加载到一半被掐断就白屏，`go()` 等 ready 选择器 60 s 超时，
 * 同一 worker 赶上一阵 veth 抖动时连着几个界面一起失败（排查见 .agent-logs 0090）。
 * 以前要先手动 `sudo bun e2e/ui-audit/netns.ts &`，忘了开就是这种假失败，被强杀后残留的包装脚本还会让下一次运行连不上服务器；
 * 现在每次运行各起一份，几路审计并行互不拆台，运行结束（或 Playwright 被强杀、stdin 关闭）时删掉。
 *
 * 非 root 开不了命名空间，照旧直接起 Chromium。包装脚本路径经环境变量 UI_AUDIT_NETNS_CHROMIUM 交给 worker
 * （worker 重新加载 playwright.config.ts 时读取）。
 */
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";

export default async function globalSetup(): Promise<
	(() => Promise<void>) | undefined
> {
	if (process.getuid?.() !== 0) return undefined;
	const child = spawn(
		"bun",
		[new URL("./netns.ts", import.meta.url).pathname],
		{
			env: {
				...process.env,
				UI_AUDIT_NETNS_NAME: `oryn-ui-audit-${process.pid}`,
			},
			stdio: ["pipe", "pipe", "inherit"],
		},
	);
	const exit = Promise.withResolvers<undefined>();
	child.once("exit", () => exit.resolve(undefined));
	child.once("error", () => exit.resolve(undefined));
	const line = Promise.withResolvers<string>();
	createInterface({ input: child.stdout }).once("line", line.resolve);
	const first = await Promise.race([line.promise, exit.promise]);
	const path = first?.match(/^ready (\S+)$/)?.[1];
	if (!path) {
		child.kill("SIGTERM");
		throw new Error(
			`没能开浏览器网络命名空间（bun e2e/ui-audit/netns.ts 输出：${first ?? "进程已退出"}）`,
		);
	}
	process.env.UI_AUDIT_NETNS_CHROMIUM = path;
	return async () => {
		child.kill("SIGTERM");
		await exit.promise;
	};
}
