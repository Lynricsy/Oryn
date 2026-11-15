/**
 * `ida` 卡片：经 `write xd://ida` 解包后按真实参数与结果文本（Core `tools/ida.ts` 的句式）渲染。
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { resolveToolRenderer } from "../registry";
import { ToolView } from "../ToolView";
import { idaRenderer } from "./ida";

interface CardOpts {
	text?: string;
	isError?: boolean;
	db?: string;
	running?: boolean;
	open?: boolean;
	/** 派发前就失败（无 inner），或调用抛错。 */
	noInner?: boolean;
}

function card(args: Record<string, unknown>, opts: CardOpts = {}) {
	const action = typeof args.action === "string" ? args.action : "exec";
	const inner = opts.noInner
		? undefined
		: { action, ...(opts.db ? { db: opts.db } : {}) };
	const result =
		opts.text === undefined
			? undefined
			: {
					content: [{ type: "text", text: opts.text }],
					details: {
						xdev: { tool: "ida", mode: "execute", args, inner },
					},
					isError: opts.isError,
				};
	return render(
		<ToolView
			name="write"
			args={{ path: "xd://ida", content: JSON.stringify(args) }}
			result={result}
			running={opts.running}
			defaultOpen={opts.open}
		/>,
	);
}

const part = (c: HTMLElement, sel: string) =>
	c.querySelector(sel) as HTMLElement | null;

const EXEC_ARGS = {
	action: "exec",
	db: "/home/me/work/bin/server",
	code: "\nfor ea, n in functions('crypt|aes'):\n    print(hex(ea), n)\nlen(functions('crypt|aes'))",
};
const EXEC_TEXT = "0x401000 aes_encrypt\n0x401200 aes_decrypt\n=> 2";
const TRACEBACK = [
	"Traceback (most recent call last):",
	'  File "<ida-exec>", line 2, in <module>',
	"NameError: name 'missing' is not defined",
].join("\n");

describe("ida 渲染器", () => {
	it("注册为 ida 工具", () => {
		expect(resolveToolRenderer("ida")).toBe(idaRenderer);
	});

	it("exec 折叠行：动作 + 代码首个非空行 + db；读数为输出行数，不展开正文", () => {
		const { container } = card(EXEC_ARGS, { text: EXEC_TEXT, db: "server" });
		expect(part(container, ".tv-name")?.textContent).toBe("xd://ida");
		const sum = part(container, ".tv-sum");
		expect(sum?.textContent).toContain("exec");
		expect(sum?.textContent).toContain("for ea, n in functions('crypt|aes'):");
		expect(sum?.textContent).not.toContain("print(hex(ea), n)");
		expect(sum?.querySelector(".tv-path")?.textContent).toBe(
			"~/work/bin/server",
		);
		expect(part(container, ".tv-meta")?.textContent).toBe("2 行");
		expect(part(container, ".tv-body")).toBeNull();
	});

	it("exec 展开：完整代码、输出与返回值分开显示，没有错误块", () => {
		const { container } = card(
			{ ...EXEC_ARGS, timeout: 30 },
			{ text: EXEC_TEXT, db: "server", open: true },
		);
		const body = part(container, ".tv-body");
		expect(body).not.toBeNull();
		const blocks = [...(body?.querySelectorAll(".tv-out") ?? [])].map((el) => ({
			title: el.querySelector(".tv-out-title")?.textContent,
			text: el.querySelector("pre")?.textContent ?? "",
		}));
		expect(blocks.map((b) => b.title)).toEqual(["代码", "输出", "返回值"]);
		expect(blocks[0]?.text).toContain("print(hex(ea), n)");
		expect(blocks[1]?.text).toBe("0x401000 aes_encrypt\n0x401200 aes_decrypt");
		expect(blocks[2]?.text).toBe("2");
		expect(body?.querySelector(".tv-pre--error")).toBeNull();
		// 完整 db 路径与超时在参数网格里；解析出的库 id 与参数不同也列出
		const kv = body?.querySelector(".tv-kv")?.textContent ?? "";
		expect(kv).toContain("~/work/bin/server");
		expect(kv).toContain("30s");
		expect(kv).toContain("库 ID");
	});

	it("exec 出错：异常类型作红色读数，traceback 用错误语气单独成块，之前的输出保留", () => {
		const { container } = card(EXEC_ARGS, {
			text: `scanning…\n${TRACEBACK}`,
			isError: true,
			db: "server",
			open: true,
		});
		expect(part(container, ".tv-card")?.className).toContain("tv-card--error");
		expect(part(container, ".tv-meta .tv-meta-err")?.textContent).toBe(
			"NameError",
		);
		const err = part(container, ".tv-body .tv-pre--error");
		expect(err?.textContent).toContain("Traceback (most recent call last):");
		expect(err?.textContent).toContain("NameError: name 'missing'");
		const titles = [
			...container.querySelectorAll(".tv-body .tv-out-title"),
		].map((el) => el.textContent);
		expect(titles).toEqual(["代码", "输出", "错误"]);
		const output = [...container.querySelectorAll(".tv-body .tv-out")][1];
		expect(output?.textContent).toContain("scanning…");
		expect(output?.textContent).not.toContain("Traceback");
	});

	it("exec 超时中断：KeyboardInterrupt 作读数与错误块", () => {
		const { container } = card(EXEC_ARGS, {
			text: "KeyboardInterrupt: interrupted (timeout or cancel)",
			isError: true,
			open: true,
		});
		expect(part(container, ".tv-meta")?.textContent).toBe("KeyboardInterrupt");
		expect(part(container, ".tv-body .tv-pre--error")?.textContent).toBe(
			"KeyboardInterrupt: interrupted (timeout or cancel)",
		);
	});

	it("exec 无输出：显示「无输出」，没有读数", () => {
		const { container } = card(EXEC_ARGS, {
			text: "(no output)",
			open: true,
		});
		expect(part(container, ".tv-meta")?.textContent).toBe("");
		expect(part(container, ".tv-body")?.textContent).toContain("无输出");
		expect(part(container, ".tv-body")?.textContent).not.toContain(
			"(no output)",
		);
	});

	it("运行中：摘要来自参数，无读数，正文给出完整参数", () => {
		const { container } = card(
			{
				action: "rename",
				db: "server",
				target: "sub_401000",
				name: "parse_header",
			},
			{ running: true, open: true },
		);
		expect(screen.getByRole("status")).toBeInTheDocument();
		const sum = part(container, ".tv-sum")?.textContent ?? "";
		expect(sum).toContain("rename");
		expect(sum).toContain("sub_401000");
		expect(sum).toContain("→ parse_header");
		expect(part(container, ".tv-meta")?.textContent).toBe("");
		const kv = part(container, ".tv-body .tv-kv")?.textContent ?? "";
		expect(kv).toContain("target");
		expect(kv).toContain("sub_401000");
		expect(kv).toContain("parse_header");
	});

	it("rename 完成：地址作读数，正文给原名，不重复结果句子", () => {
		const { container } = card(
			{ action: "rename", target: "sub_401000", name: "parse_header" },
			{
				text: "Renamed 0x401000 sub_401000 → parse_header",
				db: "server",
				open: true,
			},
		);
		expect(part(container, ".tv-meta")?.textContent).toBe("0x401000");
		const body = part(container, ".tv-body")?.textContent ?? "";
		expect(body).toContain("原名");
		expect(body).not.toContain("Renamed");
	});

	it("set_type：声明全文成 C 代码块，生效类型单独列出", () => {
		const decl = "int __fastcall parse_header(const uint8_t *buf, size_t len)";
		const { container } = card(
			{ action: "set_type", target: "parse_header", decl },
			{
				text: "Typed 0x401000 as int __fastcall(const uint8_t *buf, size_t len)",
				open: true,
			},
		);
		expect(part(container, ".tv-meta")?.textContent).toBe("0x401000");
		const decls = [...container.querySelectorAll(".tv-body .tv-out")].find(
			(el) => el.querySelector(".tv-out-title")?.textContent === "decl",
		);
		expect(decls?.textContent).toContain(decl);
		const grids = container.querySelectorAll(".tv-body .tv-kv");
		expect(grids[grids.length - 1]?.textContent).toContain(
			"生效类型int __fastcall(const uint8_t *buf, size_t len)",
		);
	});

	it("list：库数作读数，每库一行，运行中与打开中的状态作徽标", () => {
		const { container } = card(
			{ action: "list" },
			{
				text: [
					"server-1a2b  server  ELF64  x86_64/64  ~/.omp/agent/idbs/1a2b-server/server.i64  [exec running]",
					"libfoo-9f00  (opening)  ./lib/libfoo.so",
				].join("\n"),
				open: true,
			},
		);
		expect(part(container, ".tv-meta")?.textContent).toBe("2 个库");
		const rows = [...container.querySelectorAll(".tv-body .tv-row")];
		expect(rows).toHaveLength(2);
		expect(rows[0]?.querySelector(".tv-row-key")?.textContent).toBe(
			"server-1a2b",
		);
		expect(rows[0]?.textContent).toContain("exec 运行中");
		expect(rows[0]?.textContent).toContain("x86_64/64");
		expect(rows[1]?.textContent).toContain("打开中");
		expect(rows[1]?.querySelector(".tv-path")?.textContent).toBe(
			"./lib/libfoo.so",
		);
	});

	it("close 不保存：警告读数「已丢弃改动」", () => {
		const { container } = card(
			{ action: "close", db: "server-1a2b", save: false },
			{
				text: "Closed server-1a2b (discarded unsaved changes)",
				db: "server-1a2b",
			},
		);
		expect(part(container, ".tv-meta .tv-meta-warn")?.textContent).toBe(
			"已丢弃改动",
		);
	});

	it("非 exec 出错：结果文本以错误语气兜底", () => {
		const { container } = card(
			{ action: "open", db: "./nope" },
			{
				text: "db not found: ./nope",
				isError: true,
				noInner: true,
				open: true,
			},
		);
		expect(part(container, ".tv-meta")?.textContent).toBe("");
		expect(part(container, ".tv-body .tv-pre--error")?.textContent).toBe(
			"db not found: ./nope",
		);
	});
});
