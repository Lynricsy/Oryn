/**
 * `terminal` 卡片：终端画成窗口（标题栏状态灯、终端名、类别与状态，窗体是屏幕内容），
 * 只读宿主（分享页）没有「在终端面板查看」。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolRenderHost, ToolResultLike } from "../types";

const SESSION = {
	terminalId: "term-7",
	kind: "session",
	title: "web-1 部署",
	cwd: "/srv/app",
	running: true,
	state: "idle",
	host: "web-1",
};

/** 站内宿主：有 orb 文件回调，说明有终端面板可开。 */
const LIVE_HOST: ToolRenderHost = { openOrbFile: () => {} };

function ok(text: string, details: Record<string, unknown>): ToolResultLike {
	return { content: [{ type: "text", text }], details };
}

const q = (c: HTMLElement, sel: string) => c.querySelector(sel);

describe("terminal 卡片", () => {
	it("exec 完成：读数给退出码，窗口给终端名、主机、状态与屏幕，状态行退为小字", () => {
		const { container } = render(
			<ToolView
				name="terminal"
				args={{ action: "exec", id: "term-7", command: "git pull" }}
				defaultOpen
				host={LIVE_HOST}
				result={ok(
					"term-7 (ssh web-1) in /srv/app: exit 0\n\nAlready up to date.",
					{ action: "exec", exitCode: 0, terminal: SESSION },
				)}
			/>,
		);
		expect(q(container, ".tv-meta .tv-meta-ok")).toHaveTextContent("exit 0");
		expect(q(container, ".tv-term-title")).toHaveTextContent("web-1 部署");
		expect(q(container, ".tv-term-bar")).toHaveTextContent("ssh web-1");
		expect(q(container, ".tv-term-bar")).toHaveTextContent("会话");
		expect(q(container, ".tv-term-state")).toHaveTextContent("就绪");
		expect(q(container, ".tv-term-screen")).toHaveTextContent(
			"Already up to date.",
		);
		expect(q(container, ".tv-term-note")).toHaveTextContent("exit 0");
		// 有结果时入口在窗口标题栏，输入段不再重复
		expect(container.querySelectorAll(".tv-term-open")).toHaveLength(1);
		expect(q(container, ".tv-term-bar .tv-term-open")).not.toBeNull();
	});

	it("只读宿主：没有终端面板入口", () => {
		const { container } = render(
			<ToolView
				name="terminal"
				args={{ action: "read", id: "term-7" }}
				defaultOpen
				result={ok("term-7  session  idle  /srv/app  web-1 部署\n\n$ ", {
					action: "read",
					terminal: SESSION,
				})}
			/>,
		);
		expect(q(container, ".tv-term-open")).toBeNull();
		// 与标题栏重复的状态行不再作为说明出现
		expect(q(container, ".tv-term-note")).toBeNull();
	});

	it("send 按键到已退出的后台命令：读数为红色退出码，按键画成键帽", () => {
		const { container } = render(
			<ToolView
				name="terminal"
				args={{ action: "send", id: "t3", key: "ctrl-c" }}
				defaultOpen
				result={ok(
					"t3  background  exited 130  /w  bun run dev\n\n[12 earlier lines omitted; read shows the screen]\n^C",
					{
						action: "send",
						terminal: {
							terminalId: "t3",
							kind: "background",
							title: "bun run dev",
							cwd: "/w",
							running: false,
							exitCode: 130,
						},
					},
				)}
			/>,
		);
		expect(q(container, ".tv-meta .tv-meta-err")).toHaveTextContent(
			"已退出 130",
		);
		expect(q(container, ".tv-kbd")).toHaveTextContent("Ctrl+C");
		expect(q(container, ".tv-term-omitted")).toHaveTextContent(
			"省略了更早的 12 行",
		);
		expect(q(container, ".tv-term-screen")).not.toHaveTextContent("omitted");
	});

	it("list：读数给终端数，每个终端一行；站内可点开、只读不可点", () => {
		const terminals = [
			SESSION,
			{
				terminalId: "t1",
				kind: "shell",
				title: "zsh",
				cwd: "/w",
				running: true,
			},
		];
		const result = ok("…", { action: "list", terminals });
		const live = render(
			<ToolView
				name="terminal"
				args={{ action: "list" }}
				defaultOpen
				host={LIVE_HOST}
				result={result}
			/>,
		).container;
		expect(q(live, ".tv-meta")).toHaveTextContent("2 个终端");
		expect(live.querySelectorAll("button.tv-term-row")).toHaveLength(2);
		const shared = render(
			<ToolView
				name="terminal"
				args={{ action: "list" }}
				defaultOpen
				result={result}
			/>,
		).container;
		expect(shared.querySelectorAll("button.tv-term-row")).toHaveLength(0);
		expect(shared.querySelectorAll("div.tv-term-row")).toHaveLength(2);
	});

	it("list 没有终端：空状态", () => {
		const { container } = render(
			<ToolView
				name="terminal"
				args={{ action: "list" }}
				defaultOpen
				result={ok("No terminals are open.", { action: "list", terminals: [] })}
			/>,
		);
		expect(q(container, ".tv-term-empty")).toHaveTextContent("没有打开的终端");
	});

	it("出错：首行作错误提示，没有读数", () => {
		const { container } = render(
			<ToolView
				name="terminal"
				args={{ action: "exec", id: "t1", command: "ls" }}
				defaultOpen
				result={{
					content: [{ type: "text", text: "t1 is not a session you opened." }],
					isError: true,
				}}
			/>,
		);
		expect(q(container, ".tv-meta")).toBeEmptyDOMElement();
		expect(q(container, ".tv-note--err")).toHaveTextContent(
			"t1 is not a session you opened.",
		);
	});

	it("运行中：输入段给完整命令；站内给终端面板入口", () => {
		const { container } = render(
			<ToolView
				name="terminal"
				args={{ action: "exec", id: "term-7", command: "make deploy" }}
				running
				defaultOpen
				host={LIVE_HOST}
			/>,
		);
		expect(q(container, ".tv-sec--in")).toHaveTextContent("make deploy");
		expect(q(container, ".tv-sec--in .tv-term-open")).not.toBeNull();
		expect(q(container, ".tv-sum")).toHaveTextContent("执行");
	});
});
