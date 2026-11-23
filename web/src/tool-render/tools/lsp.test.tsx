/** lsp 卡片：诊断按文件分组（严重度读数）、无问题时的绿色读数、引用按文件列出并带上下文。 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

function ok(text: string, details?: unknown): ToolResultLike {
	return { content: [{ type: "text", text }], details };
}

describe("lsp 卡片", () => {
	it("诊断：头部是错误 / 警告数，展开体每条带严重度图标与位置", () => {
		const { container } = render(
			<ToolView
				name="lsp"
				args={{ action: "diagnostics", file: "src/a.ts" }}
				result={ok(
					"src/a.ts:5:2 [error] boom\nsrc/a.ts:9:1 [warning] meh\n1 error(s), 1 warning(s)",
					{ action: "diagnostics", serverName: "tsserver", success: true },
				)}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta")?.textContent).toBe(
			"1 个错误1 个警告",
		);
		expect(
			[...container.querySelectorAll(".tv-dg")].map((d) => d.className),
		).toEqual(["tv-dg tv-dg--error", "tv-dg tv-dg--warning"]);
		expect(container.querySelector(".tv-dg-pos")?.textContent).toBe("5:2");
		expect(container.querySelector(".tv-lsp-by")?.textContent).toBe("tsserver");
	});

	it("诊断为 OK：绿色「无问题」读数与说明", () => {
		const { container } = render(
			<ToolView
				name="lsp"
				args={{ action: "diagnostics", file: "src/a.ts" }}
				result={ok("OK", { action: "diagnostics", success: true })}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta-ok")?.textContent).toBe("无问题");
		expect(container.querySelector(".tv-lsp-clean")).not.toBeNull();
	});

	it("引用：按文件分组，位置所在行标为命中，上下文带行号", () => {
		const { container } = render(
			<ToolView
				name="lsp"
				args={{ action: "references", file: "src/a.ts", symbol: "f" }}
				result={ok(
					"Found 2 reference(s):\n  src/a.ts:3:1\n    2: before\n    3: f();\n    4: after\n  src/b.ts:7:4\n    7: x = f;",
					{ action: "references", success: true },
				)}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta")?.textContent).toBe("2 处");
		expect(
			[...container.querySelectorAll(".tv-fh-path")].map((p) => p.textContent),
		).toEqual(["src/a.ts", "src/b.ts"]);
		expect(
			[...container.querySelectorAll(".tv-cl-row--hit .tv-cl-no")].map(
				(n) => n.textContent,
			),
		).toEqual(["3", "7"]);
	});
});
