import { describe, expect, it } from "vitest";
import {
	filterUnmatchedOptimistic,
	parseQueueMessage,
	queueMatchKey,
} from "./queue-message";

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

	it("已有纯附件 A 时提交纯附件 B：A 不会隐藏 B，无关帧不会删掉 B，直到 B 的新权威 id 到达才消解", () => {
		const attA =
			"<oryn-attachments>\nThe user attached these files.\n" +
			"- /h/.oryn/attachments/T-1/att_01AAAAAAAAAAAAAAAAAAAAAAAA/a.pdf (application/pdf, 100 bytes)\n</oryn-attachments>";
		const attB =
			"<oryn-attachments>\nThe user attached these files.\n" +
			"- /h/.oryn/attachments/T-1/att_01BBBBBBBBBBBBBBBBBBBBBBBB/b.pdf (application/pdf, 200 bytes)\n</oryn-attachments>";
		const optB = {
			id: "opt_b",
			text: "",
			attachments: [
				{
					id: "att_01BBBBBBBBBBBBBBBBBBBBBBBB",
					name: "b.pdf",
					mime: "application/pdf",
					size: 200,
				},
			],
			baselineIds: ["s1"],
			baselineCount: 1,
		};
		const authOnlyA = [
			{ id: "s1", text: attA, editable: true, timestamp: 100 },
		];
		// 已有 A 绝不能消解 B
		expect(filterUnmatchedOptimistic([optB], authOnlyA, [attA])).toEqual([
			optB,
		]);
		// 即使新来一条同正文不同附件的 s2（仍是 a.pdf），也不能误消解 b.pdf
		const authWrongAtt = [
			...authOnlyA,
			{ id: "s2", text: attA, editable: true, timestamp: 101 },
		];
		expect(
			filterUnmatchedOptimistic([optB], authWrongAtt, [attA, attA]),
		).toEqual([optB]);
		// 真正的新权威项 s3（b.pdf）到达后才精确消解
		const authWithB = [
			...authOnlyA,
			{ id: "s3", text: attB, editable: true, timestamp: 102 },
		];
		expect(filterUnmatchedOptimistic([optB], authWithB, [attA, attB])).toEqual(
			[],
		);
	});

	it("同正文不同附件（及连发完全相同消息）：旧权威 id 不会抵消新乐观项", () => {
		const attA =
			"<oryn-attachments>\nThe user attached these files.\n" +
			"- /h/.oryn/attachments/T-1/att_01AAAAAAAAAAAAAAAAAAAAAAAA/a.md (text/markdown, 50 bytes)\n</oryn-attachments>";
		const optSameTextDiffAtt = {
			id: "opt_2",
			text: "用这个模板",
			attachments: [
				{
					id: "att_01BBBBBBBBBBBBBBBBBBBBBBBB",
					name: "b.md",
					size: 60,
				},
			],
			baselineIds: ["s1"],
			baselineCount: 1,
		};
		const optIdentical = {
			id: "opt_3",
			text: "用这个模板",
			attachments: [
				{
					id: "att_01AAAAAAAAAAAAAAAAAAAAAAAA",
					name: "a.md",
					size: 50,
				},
			],
			baselineIds: ["s1"],
			baselineCount: 1,
		};
		const authS1 = [
			{ id: "s1", text: `用这个模板\n\n${attA}`, editable: true, timestamp: 1 },
		];
		expect(
			filterUnmatchedOptimistic([optSameTextDiffAtt, optIdentical], authS1, [
				`用这个模板\n\n${attA}`,
			]),
		).toEqual([optSameTextDiffAtt, optIdentical]);
	});
});

describe("queue-message：视图回执（M33）", () => {
	const receipt =
		'<oryn-view-answer call="c1" block="0">\n出行\n- 城市: 北京\n\n```json\n{"city":"bj"}\n```\n</oryn-view-answer>';

	it("正文显示作答（去掉 JSON 围栏），带出解析后的回执", () => {
		const parsed = parseQueueMessage(receipt);
		expect(parsed.body).toBe("出行\n- 城市: 北京");
		expect(parsed.viewReceipt?.toolCallId).toBe("c1");
		expect(parsed.oryn).toBeNull();
	});

	it("正文没改时 rebuild 原样保留回执，改了就成普通消息", () => {
		const parsed = parseQueueMessage(receipt);
		expect(parsed.rebuild(parsed.body)).toBe(receipt);
		expect(parsed.rebuild("改成别的")).toBe("改成别的");
	});
});
