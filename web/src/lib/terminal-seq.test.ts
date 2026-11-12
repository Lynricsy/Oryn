import { describe, expect, it } from "vitest";
import { applyOutput } from "./terminal-seq";

describe("applyOutput", () => {
	it("丢弃已完全写过的帧（含与快照末尾同 seq 的帧）", () => {
		expect(applyOutput(10, { seq: 10, data: "abcde" })).toBe("drop");
		expect(applyOutput(10, { seq: 7, data: "abc" })).toBe("drop");
	});

	it("首尾相接时整帧写入", () => {
		expect(applyOutput(10, { seq: 13, data: "abc" })).toEqual({
			write: "abc",
			lastSeq: 13,
		});
		expect(applyOutput(0, { seq: 6, data: "fake$ " })).toEqual({
			write: "fake$ ",
			lastSeq: 6,
		});
	});

	it("与已写内容重叠时只写新增部分，不重复", () => {
		// 快照写到 10；帧覆盖 8..14
		expect(applyOutput(10, { seq: 14, data: "xyzwvu" })).toEqual({
			write: "zwvu",
			lastSeq: 14,
		});
	});

	it("中间缺帧时报告 gap", () => {
		expect(applyOutput(10, { seq: 20, data: "abc" })).toBe("gap");
	});

	it("逐帧续接得到与原文完全一致的文本", () => {
		const text = Array.from({ length: 50 }, (_, i) => `line-${i + 1}\r\n`).join(
			"",
		);
		// 快照截到第 37 个字符；之后的帧与快照有重叠
		let last = 37;
		let out = text.slice(0, last);
		for (let end = 20; end <= text.length; end += 13) {
			const from = Math.max(0, end - 20);
			const r = applyOutput(last, { seq: end, data: text.slice(from, end) });
			if (r === "gap") throw new Error("unexpected gap");
			if (r !== "drop") {
				out += r.write;
				last = r.lastSeq;
			}
		}
		const r = applyOutput(last, {
			seq: text.length,
			data: text.slice(last - 3),
		});
		if (typeof r === "object") out += r.write;
		expect(out).toBe(text);
	});
});
