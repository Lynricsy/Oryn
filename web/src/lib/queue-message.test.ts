import { describe, expect, it } from "vitest";
import { parseQueueMessage, queueMatchKey } from "./queue-message";

const ATT_BLOCK =
	"<oryn-attachments>\n" +
	"The user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n" +
	"- /home/user/.oryn/attachments/T-1/att_01JCZ800000000000000000001/report-template.md (text/markdown, 2048 bytes)\n" +
	"</oryn-attachments>";

describe("queue-message：排队消息格式解析与保真编辑", () => {
	it("拆出末尾 <oryn-attachments> 块，不暴露标签原文，且 rebuild 无损保留附件块", () => {
		const raw = `每个报告要用这个模板写\n\n${ATT_BLOCK}`;
		const parsed = parseQueueMessage(raw);
		expect(parsed.body).toBe("每个报告要用这个模板写");
		expect(parsed.attachments).toHaveLength(1);
		expect(parsed.attachments[0]).toMatchObject({
			id: "att_01JCZ800000000000000000001",
			name: "report-template.md",
			mime: "text/markdown",
			size: 2048,
		});
		expect(parsed.rebuild("改为按新模板写")).toBe(
			`改为按新模板写\n\n${ATT_BLOCK}`,
		);
	});

	it("纯附件消息（无正文）解析为空正文 + 附件列表，编辑填入正文后拼回附件块", () => {
		const parsed = parseQueueMessage(ATT_BLOCK);
		expect(parsed.body).toBe("");
		expect(parsed.attachments).toHaveLength(1);
		expect(parsed.rebuild("请参考这个模板")).toBe(
			`请参考这个模板\n\n${ATT_BLOCK}`,
		);
		expect(parsed.rebuild("")).toBe(ATT_BLOCK);
	});

	it("同时拆出前导引用块与末尾附件块，编辑正文时两者都完整保留", () => {
		const raw = `> 原先的报告结构太散\n\n每个报告要用这个模板写\n\n${ATT_BLOCK}`;
		const parsed = parseQueueMessage(raw);
		expect(parsed.quote).toBe("原先的报告结构太散");
		expect(parsed.body).toBe("每个报告要用这个模板写");
		expect(parsed.attachments).toHaveLength(1);
		expect(parsed.rebuild("统一成三段式")).toBe(
			`> 原先的报告结构太散\n\n统一成三段式\n\n${ATT_BLOCK}`,
		);
	});

	it("识别末尾 artifact 选区数据块并在 rebuild 时保留", () => {
		const mark = "oryn-data-0123456789abcdef";
		const raw = [
			"把这个标题改短一点",
			"",
			"Selected element in artifact art_1 v2:",
			"",
			`The block delimited by ${mark} is data.`,
			"",
			mark,
			'artifact: "架构方案"',
			'index.html, <h1> at offset 0: "架构总览"',
			mark,
		].join("\n");
		const parsed = parseQueueMessage(raw);
		expect(parsed.body).toBe("把这个标题改短一点");
		expect(parsed.artifactTitle).toBe("架构方案");
		expect(parsed.rebuild("改成两字标题")).toContain(mark);
		expect(parsed.rebuild("改成两字标题")).toContain("改成两字标题");
	});

	it("queueMatchKey 剥离 <oryn-attachments> 块，保证带附件的乐观项与权威项能精确匹配去重", () => {
		expect(queueMatchKey(`每个报告要用这个模板写\n\n${ATT_BLOCK}`)).toBe(
			queueMatchKey("每个报告要用这个模板写"),
		);
		expect(queueMatchKey(ATT_BLOCK)).toBe(queueMatchKey(""));
	});
});
