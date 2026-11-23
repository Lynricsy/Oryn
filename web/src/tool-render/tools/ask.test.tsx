/** ask 卡片：问卷（逐题、选项、推荐项）、回执（选中高亮、自由填写）、等待作答状态。 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";

const HTML =
	"<p>要保留旧的固定间隔吗？</p><label><input type=radio name=q1 value=keep checked>保留</label><label><input type=radio name=q1 value=drop>删除</label>" +
	"<fieldset><legend>要覆盖哪些平台？</legend><label><input type=checkbox name=os value=mac>macOS</label><label><input type=checkbox name=os value=linux>Linux</label><label><input type=checkbox name=os value=win>Windows</label></fieldset>" +
	"<label>补充说明 <textarea name=note></textarea></label>";

describe("ask 卡片", () => {
	it("问卷：逐题列出题干与选项，预选项标为推荐", () => {
		const { container } = render(
			<ToolView name="ask" args={{ html: HTML }} defaultOpen />,
		);
		const questions = [...container.querySelectorAll(".tv-ask-form > li")];
		expect(
			questions.map(
				(q) => q.querySelector(".tv-ask-qtext")?.firstChild?.textContent,
			),
		).toEqual(["要保留旧的固定间隔吗？", "要覆盖哪些平台？", "补充说明"]);
		expect(questions[0]?.querySelector(".tv-ask-rec")).not.toBeNull();
		expect(questions[1]?.querySelectorAll(".tv-ask-mark--box")).toHaveLength(3);
		expect(questions[2]?.querySelector(".tv-ask-blank")).not.toBeNull();
	});

	it("回执：选中的选项高亮、其余淡出，自由填写单列", () => {
		const { container } = render(
			<ToolView
				name="ask"
				args={{ html: HTML }}
				result={{
					content: [{ type: "text", text: "ok" }],
					details: {
						answers: [
							{
								name: "q1",
								question: "要保留旧的固定间隔吗？",
								answer: "删除",
							},
							{
								name: "os",
								question: "要覆盖哪些平台？",
								answer: "macOS, Linux",
							},
							{ name: "note", question: "补充说明", answer: "先做 Linux" },
						],
					},
				}}
				defaultOpen
			/>,
		);
		const sheet = container.querySelector(".tv-ask-sheet");
		const on = [...(sheet?.querySelectorAll(".tv-ask-opt--on") ?? [])].map(
			(el) => el.textContent,
		);
		expect(on).toEqual(["删除", "macOS", "Linux"]);
		expect(sheet?.querySelectorAll(".tv-ask-opt--off")).toHaveLength(2);
		expect(sheet?.querySelector(".tv-ask-free")).toHaveTextContent(
			"先做 Linux",
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent("已答 3 / 3");
	});

	it("等待作答：头部读数与输出段都给等待状态", () => {
		const { container } = render(
			<ToolView name="ask" args={{ html: HTML }} running defaultOpen />,
		);
		expect(container.querySelector(".tv-meta .tv-ask-wait")).toHaveTextContent(
			"等待作答",
		);
		expect(container.querySelector(".tv-ask-pending")).not.toBeNull();
	});

	it("残缺参数：没有 html 时提示参数无效", () => {
		const { container } = render(<ToolView name="ask" args={{}} />);
		expect(container.querySelector(".tv-err-text")).toHaveTextContent(
			"[invalid html]",
		);
	});
});
