import { describe, expect, it } from "vitest";
import { parseAttachmentBlock } from "./attachments";

const HEAD =
	"<oryn-attachments>\nThe user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n";
const ID = "att_01ARZ3NDEKTSV4RRFFQ69G5FAV";

describe("parseAttachmentBlock", () => {
	it("拆出控制面追加的附件块（名称含空格与括号）", () => {
		const path = `/home/user/.oryn/attachments/T-1/${ID}/报告 (v2).pdf`;
		const r = parseAttachmentBlock(
			`看看这个\n\n${HEAD}- ${path} (application/pdf, 2048 bytes)\n- /tmp/x/y.bin (application/octet-stream, 1 bytes)\n</oryn-attachments>`,
		);
		expect(r.text).toBe("看看这个");
		expect(r.attachments).toEqual([
			{
				id: ID,
				name: "报告 (v2).pdf",
				path,
				mime: "application/pdf",
				size: 2048,
			},
			{
				id: null,
				name: "y.bin",
				path: "/tmp/x/y.bin",
				mime: "application/octet-stream",
				size: 1,
			},
		]);
	});

	it("只有附件、没有正文", () => {
		const r = parseAttachmentBlock(
			`${HEAD}- /h/.oryn/attachments/T-1/${ID}/a.png (image/png, 3 bytes)\n</oryn-attachments>`,
		);
		expect(r.text).toBe("");
		expect(r.attachments).toHaveLength(1);
	});

	it("没有附件块或附件块不在末尾时原样返回", () => {
		const plain = "hello <oryn-attachments> not a block";
		expect(parseAttachmentBlock(plain)).toEqual({
			text: plain,
			attachments: [],
		});
		const mid = `${HEAD}- /a (x/y, 1 bytes)\n</oryn-attachments>\nmore`;
		expect(parseAttachmentBlock(mid).attachments).toEqual([]);
	});
});
