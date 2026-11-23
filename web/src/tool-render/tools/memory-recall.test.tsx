/** recall 卡片：两种记忆后端的命中格式都拆成记忆卡片，没有命中给空状态。 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";

function recall(text: string) {
	return render(
		<ToolView
			name="recall"
			args={{ query: "重连" }}
			result={{ content: [{ type: "text", text }], details: {} }}
			defaultOpen
		/>,
	);
}

describe("recall 卡片", () => {
	it("mnemopi 格式：拆出 id、来源、日期与相关度", () => {
		const { container } = recall(
			"Found 1 relevant memory (as of 2026-11-12 09:00 UTC):\n\n- 退避上限 15s (id: m_42) [decision] (2026-11-02) c:0.8",
		);
		const card = container.querySelector(".tv-mem");
		expect(card?.querySelector(".tv-mem-text")).toHaveTextContent(
			/^退避上限 15s$/,
		);
		expect(card?.querySelector(".tv-mem-tag")).toHaveTextContent("decision");
		expect(card?.querySelector(".tv-mem-date")).toHaveTextContent("2026-11-02");
		expect(card?.querySelector(".tv-mem-id")).toHaveTextContent("m_42");
		expect(card?.querySelector(".tv-mem-score")).toHaveTextContent("0.8");
		expect(container.querySelector(".tv-meta")).toHaveTextContent("1 条");
	});

	it("hindsight 格式：类型与日期", () => {
		const { container } = recall(
			"Found 2 relevant memories (as of 2026-11-12 09:00 UTC):\n\n- 订阅恢复用 afterSeq [convention] (2026-10-28)\n\n- 第二条",
		);
		const cards = container.querySelectorAll(".tv-mem");
		expect(cards).toHaveLength(2);
		expect(cards[0]?.querySelector(".tv-mem-tag")).toHaveTextContent(
			"convention",
		);
		expect(cards[1]?.querySelector(".tv-mem-foot")).toBeNull();
	});

	it("没有命中：头部警告读数 + 空状态", () => {
		const { container } = recall("No relevant memories found.");
		expect(container.querySelector(".tv-meta .tv-meta-warn")).toHaveTextContent(
			"无命中",
		);
		expect(container.querySelector(".tv-mem")).toBeNull();
		expect(container.querySelector(".tv-sec--out")).toHaveTextContent(
			"没有相关的记忆",
		);
	});
});
