import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";

/** `write xd://security_scan` 派发完成（或运行中）的卡片。 */
function card(
	args: Record<string, unknown>,
	result?: { text: string; inner?: unknown; isError?: boolean },
	open = true,
) {
	return render(
		<ToolView
			name="write"
			args={{ path: "xd://security_scan", content: JSON.stringify(args) }}
			result={
				result && {
					content: [{ type: "text", text: result.text }],
					details: {
						xdev: {
							tool: "security_scan",
							mode: "execute",
							args,
							inner: result.inner,
						},
					},
					isError: result.isError,
				}
			}
			running={!result}
			defaultOpen={open}
		/>,
	);
}

const q = (c: HTMLElement, sel: string) => c.querySelector(sel);

describe("security_scan 卡片", () => {
	it("preflight：摘要给目标范围，读数给计划就绪，展开体给计划 id 与指纹", () => {
		const { container } = card(
			{
				action: "preflight",
				target_kind: "ref_diff",
				base_revision: "main",
				head_revision: "HEAD",
			},
			{
				text: "Plan sp-1 ready.",
				inner: {
					action: "preflight",
					plan: { id: "sp-1", fingerprint: "f00dcafe" },
				},
			},
		);
		expect(q(container, ".tv-name")).toHaveTextContent("xd://security_scan");
		expect(q(container, ".tv-sum")).toHaveTextContent("preflight");
		expect(q(container, ".tv-sum")).toHaveTextContent("main");
		expect(q(container, ".tv-meta .tv-meta-ok")).toHaveTextContent("计划就绪");
		const body = q(container, ".tv-body");
		expect(body).toHaveTextContent("sp-1");
		expect(body).toHaveTextContent("f00dcafe");
		// 有结构化结果时不再堆结果原文
		expect(body).not.toHaveTextContent("Plan sp-1 ready.");
	});

	it("status：读数给阶段与发现数，展开体给 operation 快照与错误", () => {
		const { container } = card(
			{ action: "status", operation_id: "op-9" },
			{
				text: "op-9 failed",
				inner: {
					action: "status",
					operation: {
						operationId: "op-9",
						planId: "sp-1",
						scanId: "scan-3",
						phase: "failed",
						createdAt: "2026-11-15T08:00:00Z",
						updatedAt: "2026-11-15T08:20:00Z",
						findingCount: 2,
						error: "model quota exhausted",
					},
				},
			},
		);
		expect(q(container, ".tv-sum")).toHaveTextContent("op-9");
		expect(q(container, ".tv-meta .tv-meta-err")).toHaveTextContent("failed");
		expect(q(container, ".tv-meta .tv-meta-warn")).toHaveTextContent(
			"2 个发现",
		);
		const body = q(container, ".tv-body");
		expect(body).toHaveTextContent("scan-3");
		expect(body?.querySelector(".tv-err-text")).toHaveTextContent(
			"model quota exhausted",
		);
	});

	it("validate 运行中：读数给请求的校验状态，展开体给摘要与证据", () => {
		const { container } = card({
			action: "validate",
			finding_id: "F-12",
			validation_status: "validated",
			validation_summary: "可从公网触发",
			validation_evidence: [{ label: "repro", explanation: "curl 复现" }],
		});
		expect(q(container, ".tv-sum")).toHaveTextContent("F-12");
		expect(q(container, ".tv-meta")).toHaveTextContent("validated");
		const body = q(container, ".tv-body");
		expect(body?.querySelector(".tv-prose")).toHaveTextContent("可从公网触发");
		expect(body).toHaveTextContent("curl 复现");
	});

	it("出错时无读数，展开体给出错误文本", () => {
		const { container } = card(
			{ action: "start", plan_id: "sp-404" },
			{ text: "Unknown plan sp-404", isError: true },
		);
		expect(q(container, ".tv-card--error")).not.toBeNull();
		expect(q(container, ".tv-meta")).toBeEmptyDOMElement();
		expect(q(container, ".tv-body")).toHaveTextContent("Unknown plan sp-404");
	});
});
