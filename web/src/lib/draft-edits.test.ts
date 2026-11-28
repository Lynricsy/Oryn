import { describe, expect, it } from "vitest";
import { parseAttachmentBlock } from "./attachments";
import {
	buildDraftEditsBlock,
	DRAFT_EDITS_HEAD,
	parseDraftEditsBlock,
} from "./draft-edits";

const ATT_BLOCK =
	"<oryn-attachments>\nThe user attached these files.\n- /home/user/.oryn/attachments/T-1/att_01JCZ800000000000000000001/a.md (text/markdown, 12 bytes)\n</oryn-attachments>";

describe("draft-edits", () => {
	it("拼出的块与 C3 逐字一致", () => {
		expect(
			buildDraftEditsBlock([
				{ kind: "email", title: "周五会议改期", text: "Hi Sam,\n\nThanks" },
			]),
		).toBe(
			`\n\n${DRAFT_EDITS_HEAD}<draft kind="email" title="周五会议改期">\nHi Sam,\n\nThanks\n</draft>\n</oryn-draft-edits>`,
		);
		expect(buildDraftEditsBlock([])).toBe("");
	});

	it("build → parse 往返，属性与正文转义", () => {
		const block = buildDraftEditsBlock([
			{ kind: "doc", title: 'A & "B" <C>', text: "x </draft> y" },
			{ kind: "message", title: "", text: "</oryn-draft-edits>" },
		]);
		expect(block).toContain('title="A &amp; &quot;B&quot; &lt;C>"');
		expect(block).toContain("x <\\/draft> y");
		expect(block).toContain("<\\/oryn-draft-edits>\n</draft>");
		const parsed = parseDraftEditsBlock(`改一下${block}`);
		expect(parsed.text).toBe("改一下");
		expect(parsed.drafts).toEqual([
			{ kind: "doc", title: 'A & "B" <C>' },
			{ kind: "message", title: "" },
		]);
	});

	it("没有改稿块时原样返回", () => {
		expect(parseDraftEditsBlock("你好")).toEqual({ text: "你好", drafts: [] });
	});

	it("与附件块并存：先剥附件再剥改稿", () => {
		const block = buildDraftEditsBlock([
			{ kind: "email", title: "T", text: "body" },
		]);
		const raw = `正文${block}\n\n${ATT_BLOCK}`;
		// 附件块在最后：不先剥附件就认不出改稿块
		expect(parseDraftEditsBlock(raw).drafts).toEqual([]);
		const att = parseAttachmentBlock(raw);
		expect(att.attachments).toHaveLength(1);
		const parsed = parseDraftEditsBlock(att.text);
		expect(parsed.text).toBe("正文");
		expect(parsed.drafts).toEqual([{ kind: "email", title: "T" }]);
	});

	it("只有改稿块（正文为空）时也能解析", () => {
		const block = buildDraftEditsBlock([
			{ kind: "message", title: "M", text: "hi" },
		]).replace(/^\n+/, "");
		expect(parseDraftEditsBlock(block)).toEqual({
			text: "",
			drafts: [{ kind: "message", title: "M" }],
		});
	});
});
