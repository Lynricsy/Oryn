#!/usr/bin/env bun
/**
 * UI 审计用的只有回环的浏览器网络命名空间（需要 root），由 global-setup.ts 每次运行起一份，不用手动开。
 *
 * 本机 Docker orb 的 veth 不停增删，Chromium 收到网卡变化会中止在途请求（net::ERR_NETWORK_CHANGED），
 * 并行跑时模块图加载到一半被掐断，页面白屏（见 scripts/browser-netns.ts）。
 * 命名空间名经 UI_AUDIT_NETNS_NAME 传入；就绪后输出 `ready <包装脚本路径>`，常驻到 SIGINT / SIGTERM 或 stdin 关闭
 * （Playwright 主进程被强杀时），退出前删掉命名空间。
 */
import { startBrowserNetns } from "../../scripts/browser-netns";

const name = process.env.UI_AUDIT_NETNS_NAME;
if (!name)
	throw new Error("缺 UI_AUDIT_NETNS_NAME：本脚本由 global-setup.ts 启动");
const ns = await startBrowserNetns({
	name,
	// after / before 两个审计服务器
	ports: [5293, 5294],
	chromium: process.env.CHROMIUM ?? "/usr/bin/chromium",
});
console.log(`ready ${ns.executablePath}`);
const exit = () => {
	ns.stop();
	process.exit(0);
};
for (const sig of ["SIGINT", "SIGTERM"] as const) process.on(sig, exit);
process.stdin.on("close", exit);
process.stdin.resume();
