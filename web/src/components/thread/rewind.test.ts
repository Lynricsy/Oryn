import { describe, expect, it } from "vitest";
import type { Attachment } from "../../lib/attachments";
import { draftFromLeafSwitch } from "./rewind";

const PNG = "att_01HZZZZZZZZZZZZZZZZZZZZZZ1";
const PDF = "att_01HZZZZZZZZZZZZZZZZZZZZZZ2";

function att(id: string, name: string, mime: string): Attachment {
	return {
		id,
		threadId: "T-1",
		name,
		mime,
		size: 10,
		sha256: "0".repeat(64),
		createdAt: 0,
	};
}

const block = (...lines: string[]) =>
	`\n\n<oryn-attachments>\n${lines.join("\n")}\n</oryn-attachments>`;

const threadAttachments = [
	att(PNG, "shot.png", "image/png"),
	att(PDF, "spec.pdf", "application/pdf"),
];

const inline = { type: "image" as const, data: "QUJD", mimeType: "image/webp" };

describe("draftFromLeafSwitch", () => {
	it("去掉附件块，按 id 复用线程附件；图片与内联副本个数相等时配回同一项", () => {
		const { text, items } = draftFromLeafSwitch(
			{
				editorText: `看看这个${block(
					`- /home/u/.oryn/attachments/${PNG}/shot.png (image/png, 10 bytes)`,
					`- /home/u/.oryn/attachments/${PDF}/spec.pdf (application/pdf, 10 bytes)`,
				)}`,
				editorImages: [inline],
			},
			threadAttachments,
		);
		expect(text).toBe("看看这个");
		expect(items).toEqual([
			{
				name: "shot.png",
				size: 10,
				attachment: threadAttachments[0],
				image: inline,
			},
			{ name: "spec.pdf", size: 10, attachment: threadAttachments[1] },
		]);
	});

	it("图片个数对不上时图片只作为文件放回，不配错内联副本", () => {
		const { items } = draftFromLeafSwitch(
			{
				editorText: block(
					`- /a/${PNG}/shot.png (image/png, 10 bytes)`,
				).trimStart(),
				editorImages: [inline, inline],
			},
			threadAttachments,
		);
		expect(items).toEqual([
			{ name: "shot.png", size: 10, attachment: threadAttachments[0] },
		]);
	});

	it("没有线程附件的图片（只随 images 发送）作为内联图片放回", () => {
		const { text, items } = draftFromLeafSwitch(
			{ editorText: "改一下", editorImages: [inline] },
			[],
		);
		expect(text).toBe("改一下");
		expect(items).toEqual([{ name: "image-1.webp", size: 3, image: inline }]);
	});
});
