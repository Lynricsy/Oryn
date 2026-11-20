#!/usr/bin/env bun
/**
 * 给 UI 审计开一个只有回环的浏览器网络命名空间（需要 root），常驻到 Ctrl+C / SIGTERM。
 *
 * 本机 Docker orb 的 veth 不停增删，Chromium 收到网卡变化会中止在途请求（net::ERR_NETWORK_CHANGED），
 * 并行跑时模块图加载到一半被掐断，页面白屏（见 scripts/browser-netns.ts）。开着本脚本时，
 * playwright.config.ts 会自动改用 `/run/oryn-ui-audit/chromium.sh` 在命名空间里启动 Chromium。
 *
 *   sudo bun e2e/ui-audit/netns.ts &
 */
import { startBrowserNetns } from "../../scripts/browser-netns";

const ns = await startBrowserNetns({
	name: "oryn-ui-audit",
	// after / before 两个审计服务器
	ports: [5293, 5294],
	chromium: process.env.CHROMIUM ?? "/usr/bin/chromium",
});
console.log(`ready ${ns.executablePath}`);
for (const sig of ["SIGINT", "SIGTERM"] as const)
	process.on(sig, () => {
		ns.stop();
		process.exit(0);
	});
