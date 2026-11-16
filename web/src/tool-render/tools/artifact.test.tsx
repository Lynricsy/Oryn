/**
 * artifact 工具的转录卡片（M14 2.4）：摘要文案、正文里的 artifact 卡片只取 `details`、点击交给宿主、只读时不可点；
 * 「agent 工具」表里的全部工具都注册到这个渲染器。
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { resolveToolRenderer } from "../registry";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";
import { ARTIFACT_TOOLS, artifactRenderer } from "./artifact";

const ID = "art_01JDNAWEB0000000000000000A";

const result = (details: unknown, isError = false): ToolResultLike => ({
	content: [{ type: "text", text: JSON.stringify(details ?? null) }],
	details,
	isError,
});

function card(op: string, extra: Record<string, unknown> = {}) {
	return {
		op,
		id: ID,
		title: "本周习惯打卡",
		kind: "web",
		version: 1,
		...extra,
	};
}

describe("artifact 工具渲染器", () => {
	it("「agent 工具」表里的全部工具都注册到它", () => {
		expect(ARTIFACT_TOOLS).toHaveLength(14);
		for (const name of ARTIFACT_TOOLS)
			expect(resolveToolRenderer(name)).toBe(artifactRenderer);
	});

	it("摘要：创建 / 更新 / 只改名 / 恢复 / 删除", () => {
		const cases: [string, unknown, Record<string, unknown>, string][] = [
			["create_artifact", card("create_artifact"), {}, "页面 · 已创建 · v1"],
			[
				"update_artifact",
				card("update_artifact", { version: 3, fromVersion: 2 }),
				{},
				"已更新 · v2→v3",
			],
			[
				"update_artifact",
				card("update_artifact"),
				{ title: "新标题" },
				"已改名",
			],
			[
				"restore_artifact_version",
				card("restore_artifact_version", { version: 4, fromVersion: 1 }),
				{},
				"已恢复 · v1",
			],
			["delete_artifact", card("delete_artifact"), {}, "已删除"],
		];
		for (const [name, details, args, want] of cases) {
			const { unmount } = render(
				<ToolView
					name={name}
					args={{ id: ID, ...args }}
					result={result(details)}
				/>,
			);
			expect(document.body).toHaveTextContent(want);
			unmount();
		}
	});

	it("读类工具显示工具名与目标标题；出错时不显示已创建", () => {
		const { unmount } = render(
			<ToolView
				name="read_artifact"
				args={{ id: ID }}
				result={result(card("read_artifact"))}
			/>,
		);
		expect(document.body).toHaveTextContent(
			`${i18n.t("artifacts.tool.names.read_artifact")} 本周习惯打卡`,
		);
		unmount();
		render(
			<ToolView
				name="create_artifact"
				args={{ title: "草稿" }}
				result={result(
					{ op: "create_artifact", error: { code: "invalid", message: "x" } },
					true,
				)}
			/>,
		);
		expect(document.body).not.toHaveTextContent("已创建");
		expect(document.body).toHaveTextContent("草稿");
	});

	it("正文的卡片只用 details，点击交给 host.openArtifact；没有宿主能力（只读）时不可点", () => {
		const open = vi.fn();
		const fetchSpy = vi.spyOn(globalThis, "fetch");
		const details = card("update_artifact", { version: 2, fromVersion: 1 });
		const { unmount } = render(
			<ToolView
				name="update_artifact"
				args={{ id: ID }}
				result={result(details)}
				defaultOpen
				host={{ openArtifact: open }}
			/>,
		);
		const button = screen.getByRole("button", {
			name: i18n.t("artifacts.card.open", { title: "本周习惯打卡" }),
		});
		expect(button).toHaveTextContent("v2");
		fireEvent.click(button);
		expect(open).toHaveBeenCalledWith(ID);
		expect(fetchSpy).not.toHaveBeenCalled();
		unmount();

		render(
			<ToolView
				name="update_artifact"
				args={{ id: ID }}
				result={result(details)}
				defaultOpen
			/>,
		);
		expect(screen.getByTestId("artifact-card").tagName).toBe("DIV");
		expect(
			screen.queryByRole("button", {
				name: i18n.t("artifacts.card.open", { title: "本周习惯打卡" }),
			}),
		).toBeNull();
		fetchSpy.mockRestore();
	});

	it("details 残缺时不渲染卡片，退回通用渲染", () => {
		render(
			<ToolView
				name="create_artifact"
				args={{ title: "x" }}
				result={result({ op: "create_artifact", id: 3 })}
				defaultOpen
			/>,
		);
		expect(screen.queryByTestId("artifact-tool-card")).toBeNull();
	});
});
