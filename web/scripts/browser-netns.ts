/**
 * 浏览器跑在只有回环的独立网络命名空间里（验收与截图脚本共用；排查过程见 .agent-logs 0090）。
 *
 * Chromium 监听内核 netlink 的网卡 / 地址变化，一有变化就中止全部在途请求（net::ERR_NETWORK_CHANGED）；本机别的负载
 * 不停创建、销毁 Docker orb 的 veth，模块图加载到一半被中止，页面白屏、等待超时。命名空间里看不到宿主的网卡变化。
 * 浏览器要访问的端口经文件系统 unix 套接字转发（这类套接字跨网络命名空间可用）：命名空间内的转发进程在 127.0.0.1 / ::1
 * 上监听同样的端口并连到套接字，本进程把套接字连到宿主的 127.0.0.1。
 *
 * 需要 root（`ip netns`、`nsenter`）。
 */
import { chmodSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { connect, createServer, type Server, type Socket } from "node:net";
import type { Subprocess } from "bun";

export interface BrowserNetns {
	/** 在命名空间里启动 Chromium 的包装脚本，作 `chromium.launch({ executablePath })`。 */
	executablePath: string;
	/** 关掉转发、删除命名空间与套接字目录；可重复调用。 */
	stop(): void;
}

/**
 * 双向接上两条连接。一端正常关闭时由 pipe 把另一端 `end()`：先写完缓冲里的数据再发 FIN；只有出错才两端一起销毁。
 * 不能在 `close` 时就 destroy 另一端：上游写完大响应随即关闭时，读得慢的浏览器那端缓冲里还压着没发完的字节，
 * destroy 会把它们丢掉（浏览器报 ERR_CONTENT_LENGTH_MISMATCH，K24 的文档查看器 document.js 偶发截断即此）。
 */
function splice(a: Socket, b: Socket) {
	a.pipe(b);
	b.pipe(a);
	const kill = () => {
		a.destroy();
		b.destroy();
	};
	a.on("error", kill);
	b.on("error", kill);
}

/** 建好命名空间 `name` 与 `ports` 的端口转发，返回启动 `chromium` 的包装脚本。 */
export async function startBrowserNetns({
	name,
	ports,
	chromium,
}: {
	name: string;
	ports: readonly number[];
	chromium: string;
}): Promise<BrowserNetns> {
	const dir = `/run/${name}`;
	const servers: Server[] = [];
	let forward: Subprocess | undefined;
	const stop = () => {
		for (const s of servers.splice(0)) s.close();
		forward?.kill("SIGTERM");
		forward = undefined;
		Bun.spawnSync(["ip", "netns", "del", name], { stderr: "ignore" });
		rmSync(dir, { recursive: true, force: true });
	};
	try {
		mkdirSync(dir, { recursive: true });
		for (const cmd of [
			["ip", "netns", "add", name],
			["ip", "-n", name, "link", "set", "lo", "up"],
		]) {
			const r = Bun.spawnSync(cmd, { stderr: "pipe" });
			if (r.exitCode !== 0)
				throw new Error(`${cmd.join(" ")}：${r.stderr.toString().trim()}`);
		}
		for (const port of ports)
			servers.push(
				createServer((c) => splice(c, connect(port, "127.0.0.1"))).listen(
					`${dir}/${port}.sock`,
				),
			);
		const fwd = `${dir}/forward.mjs`;
		writeFileSync(
			fwd,
			`import { connect, createServer } from "node:net";
for (const port of ${JSON.stringify(ports)})
	for (const host of ["127.0.0.1", "::1"])
		createServer((c) => {
			// 与 splice 同：正常关闭交给 pipe 排空后 end，只有出错才销毁两端
			const u = connect(${JSON.stringify(dir)} + "/" + port + ".sock");
			c.pipe(u);
			u.pipe(c);
			const kill = () => { c.destroy(); u.destroy(); };
			c.on("error", kill);
			u.on("error", kill);
		}).listen(port, host);
console.log("ready");
`,
		);
		forward = Bun.spawn(["ip", "netns", "exec", name, "bun", fwd], {
			stdout: "pipe",
			stderr: "inherit",
		});
		const first = await forward.stdout.getReader().read();
		if (!new TextDecoder().decode(first.value).includes("ready"))
			throw new Error("浏览器网络命名空间的端口转发没有就绪");
		const wrapper = `${dir}/chromium.sh`;
		writeFileSync(
			wrapper,
			`#!/bin/sh\nexec nsenter --net=/run/netns/${name} ${chromium} "$@"\n`,
		);
		chmodSync(wrapper, 0o755);
		return { executablePath: wrapper, stop };
	} catch (e) {
		stop();
		throw e;
	}
}
