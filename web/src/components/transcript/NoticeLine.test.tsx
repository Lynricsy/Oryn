import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Notice } from "../../state/thread-reducer";
import { NoticeLine, noticeBodyFormat } from "./NoticeLine";

function notice(over: Partial<Notice>): Notice {
	return {
		key: "n1",
		afterSeq: 0,
		kind: "command_output",
		level: "info",
		text: "",
		ts: 0,
		...over,
	};
}

/** Core `/changelog` 的输出形状：标题 + 小节 + 列表，足够长到折叠。 */
const CHANGELOG = [
	"## [18.19.0] - 2026-11-10",
	"",
	"### Added",
	"",
	...Array.from({ length: 20 }, (_, i) => `- change ${i + 1}`),
].join("\n");

/** Core `/jobs`：两行说明 + 围栏代码块里的树形导轨。 */
const JOBS = [
	"Background Jobs",
	"Running: 1",
	"",
	"```text",
	"└─ [bg-1] bash (running) — 2m",
	"     Building web bundle",
	"```",
].join("\n");

/** Core `/context`：没有围栏、靠 padEnd 对齐的纯文本。 */
const CONTEXT = [
	"Context window: 200000 tokens (38% used)",
	"  System prompt    ###.................  29870 tokens",
	"  Tools            ##..................  18044 tokens",
].join("\n");

describe("noticeBodyFormat", () => {
	it("Markdown 标题、列表、围栏代码块按 Markdown 排", () => {
		expect(noticeBodyFormat(CHANGELOG)).toBe("markdown");
		// 框线字符在围栏里：围栏本来就等宽，整体仍是 Markdown
		expect(noticeBodyFormat(JOBS)).toBe("markdown");
	});

	it("两行以上词间多空格对齐、或围栏外有框线 / 制表符 → 等宽纯文本", () => {
		expect(noticeBodyFormat(CONTEXT)).toBe("plain");
		expect(noticeBodyFormat("Jobs\n├─ a\n└─ b")).toBe("plain");
		expect(noticeBodyFormat("name\tvalue")).toBe("plain");
	});

	it("单行里偶然的双空格、补齐空格的 Markdown 表格不算对齐文本", () => {
		expect(noticeBodyFormat("Done.  Next turn uses bash.")).toBe("markdown");
		expect(
			noticeBodyFormat("| a    | b   |\n| ---- | --- |\n| 1    | 2   |"),
		).toBe("markdown");
	});
});

describe("NoticeLine", () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("命令输出的 Markdown 渲染成标题与列表，不显示原文记号", () => {
		const { container } = render(
			<NoticeLine notice={notice({ text: CHANGELOG })} />,
		);
		expect(container.querySelector("h2")?.textContent).toBe(
			"[18.19.0] - 2026-11-10",
		);
		expect(container.querySelectorAll("li")).toHaveLength(20);
		expect(container.textContent).not.toContain("##");
		expect(screen.getByText("命令输出")).toBeInTheDocument();
	});

	it("围栏代码块（/jobs）保留树形导轨的原样排版", () => {
		const { container } = render(
			<NoticeLine notice={notice({ text: JOBS })} />,
		);
		expect(container.querySelector("pre")?.textContent).toContain(
			"└─ [bg-1] bash (running) — 2m\n     Building web bundle",
		);
		expect(container.textContent).not.toContain("```");
	});

	it("对齐的纯文本放进等宽、不折行的 pre，空格原样保留", () => {
		const { container } = render(
			<NoticeLine notice={notice({ text: CONTEXT })} />,
		);
		const pre = container.querySelector("pre");
		expect(pre?.textContent).toBe(CONTEXT);
		expect(pre).toHaveClass("whitespace-pre", "font-mono", "overflow-x-auto");
		expect(container.querySelector(".md")).toBeNull();
	});

	it("单行短输出接在标签后面，没有正文卡片；尖括号原样显示", () => {
		const { container } = render(
			<NoticeLine
				notice={notice({
					text: "Usage: /jobs [full|kill <id>|kill all] or `a<b`",
				})}
			/>,
		);
		expect(screen.queryByTestId("notice-body")).toBeNull();
		expect(container.querySelector(".md")?.textContent).toBe(
			"Usage: /jobs [full|kill <id>|kill all] or a<b",
		);
		// 行内代码里不转义（反斜杠在代码里是字面量）
		expect(container.querySelector("code")?.textContent).toBe("a<b");
	});

	it("其它种类的提示正文同样按 Markdown 渲染", () => {
		const { container } = render(
			<NoticeLine
				notice={notice({
					kind: "notice",
					text: "Fast mode needs `/fast on` first",
				})}
			/>,
		);
		expect(container.querySelector("code")?.textContent).toBe("/fast on");
	});

	it("超出最大高度时折叠，可以展开全部再收起", () => {
		vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(900);
		vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(320);
		const scrollIntoView = vi.fn();
		Element.prototype.scrollIntoView = scrollIntoView;
		render(<NoticeLine notice={notice({ text: CHANGELOG })} />);
		const body = screen.getByTestId("notice-body");
		expect(body).toHaveClass("max-h-80");
		expect(body).toHaveAttribute("data-folded", "true");
		const toggle = screen.getByRole("button", { name: "展开全部（24 行）" });
		expect(toggle).toHaveAttribute("aria-expanded", "false");

		fireEvent.click(toggle);
		expect(body).not.toHaveClass("max-h-80");
		expect(body).not.toHaveAttribute("data-folded");
		const less = screen.getByRole("button", { name: "收起" });
		expect(less).toHaveAttribute("aria-expanded", "true");

		fireEvent.click(less);
		expect(body).toHaveClass("max-h-80");
		expect(
			screen.getByRole("button", { name: "展开全部（24 行）" }),
		).toBeInTheDocument();
		// 收起后把卡片拉回视口（只在收起时，不在展开时）
		expect(scrollIntoView).toHaveBeenCalledTimes(1);
		expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
		Reflect.deleteProperty(Element.prototype, "scrollIntoView");
	});

	it("没有超出最大高度时不折叠、不显示展开按钮", () => {
		render(<NoticeLine notice={notice({ text: CHANGELOG })} />);
		expect(screen.getByTestId("notice-body")).not.toHaveAttribute(
			"data-folded",
		);
		expect(screen.queryByRole("button", { name: /展开全部/ })).toBeNull();
	});

	it("复制按钮复制完整原文", async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, "clipboard", {
			value: { writeText },
			configurable: true,
		});
		render(<NoticeLine notice={notice({ text: CONTEXT })} />);
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "复制命令输出" }));
		});
		expect(writeText).toHaveBeenCalledWith(CONTEXT);
	});

	it("信息 / 警告 / 错误三档色调：图标方块、标签、卡片描边", () => {
		const { container, rerender } = render(
			<NoticeLine notice={notice({ kind: "notice", text: "a\nb" })} />,
		);
		const line = () => container.querySelector("[data-notice]");
		const label = () => screen.getByText("提示");
		expect(label()).toHaveClass("text-muted");
		expect(screen.getByTestId("notice-body").parentElement).toHaveClass(
			"border-border",
		);
		expect(line()).toHaveAttribute("data-level", "info");

		rerender(
			<NoticeLine
				notice={notice({ kind: "notice", level: "warning", text: "a\nb" })}
			/>,
		);
		expect(line()).toHaveAttribute("data-level", "warning");
		expect(label()).toHaveClass("text-warn-ink");
		expect(screen.getByTestId("notice-body").parentElement).toHaveClass(
			"border-warn/40",
		);

		rerender(
			<NoticeLine
				notice={notice({ kind: "notice", level: "error", text: "a\nb" })}
			/>,
		);
		expect(line()).toHaveAttribute("data-level", "error");
		expect(label()).toHaveClass("text-err");
		expect(screen.getByTestId("notice-body").parentElement).toHaveClass(
			"border-err/35",
		);
	});

	it("没有正文的提示只有一行标签，没有复制按钮", () => {
		render(<NoticeLine notice={notice({ kind: "compaction_done" })} />);
		expect(screen.getByText("上下文已压缩")).toBeInTheDocument();
		expect(screen.queryByTestId("notice-copy")).toBeNull();
		expect(screen.queryByTestId("notice-body")).toBeNull();
	});

	it("open_url 显示可点的链接", () => {
		render(
			<NoticeLine
				notice={notice({
					kind: "open_url",
					text: "Sign in to continue",
					detail: { url: "https://example.com/login" },
				})}
			/>,
		);
		const link = screen.getByRole("link", {
			name: "https://example.com/login",
		});
		expect(link).toHaveAttribute("href", "https://example.com/login");
		expect(link).toHaveAttribute("target", "_blank");
	});

	it("/mcp list 的 ` | ` 分隔行渲染成表格（单行也不内联）", () => {
		const { container } = render(
			<NoticeLine
				notice={notice({
					text: "fs_tools | stdio | disabled | npx -y @a/b_c [project]",
				})}
			/>,
		);
		const table = container.querySelector('[data-config-list="mcp"] table');
		const cells = [...(table?.querySelectorAll("tbody td") ?? [])].map(
			(td) => td.textContent,
		);
		expect(cells[0]).toBe("fs_tools");
		expect(cells[1]).toBe("stdio");
		expect(cells[3]).toBe("npx -y @a/b_c");
	});
});
