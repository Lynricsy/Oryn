/**
 * artifact 的下载（计划 M14 2.7 / 9.2 的移动壳限制）：一律经 `<a download>`（服务端地址或同源 `blob:`），不用 `window.open`。
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { dispositionName, downloadBlob, downloadUrl } from "./download";

afterEach(() => {
	vi.restoreAllMocks();
	vi.useRealTimers();
});

function captureClicks(): { href: string; download: string; inDoc: boolean }[] {
	const clicks: { href: string; download: string; inDoc: boolean }[] = [];
	vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
		this: HTMLAnchorElement,
	) {
		clicks.push({
			href: this.href,
			download: this.download,
			inDoc: document.body.contains(this),
		});
	});
	return clicks;
}

describe("download", () => {
	it("服务端地址：临时 <a download> 点一下就移除，不调 window.open", () => {
		const open = vi.spyOn(window, "open");
		const clicks = captureClicks();
		downloadUrl("/api/v1/artifacts/art_1/export?format=zip", "报告.zip");
		expect(clicks).toEqual([
			{
				href: `${location.origin}/api/v1/artifacts/art_1/export?format=zip`,
				download: "报告.zip",
				inDoc: true,
			},
		]);
		expect(document.querySelector("a[download]")).toBeNull();
		expect(open).not.toHaveBeenCalled();
	});

	it("内存里的内容：同源 blob: 地址，稍后释放", () => {
		vi.useFakeTimers();
		const open = vi.spyOn(window, "open");
		const saved = {
			createObjectURL: URL.createObjectURL,
			revokeObjectURL: URL.revokeObjectURL,
		};
		const revoke = vi.fn();
		Object.assign(URL, {
			createObjectURL: () => `blob:${location.origin}/x`,
			revokeObjectURL: revoke,
		});
		try {
			const clicks = captureClicks();
			downloadBlob(new Blob(["hi"]), "报告.docx");
			expect(clicks.map((c) => [c.href, c.download])).toEqual([
				[`blob:${location.origin}/x`, "报告.docx"],
			]);
			expect(revoke).not.toHaveBeenCalled();
			vi.advanceTimersByTime(60_000);
			expect(revoke).toHaveBeenCalledWith(`blob:${location.origin}/x`);
			expect(open).not.toHaveBeenCalled();
		} finally {
			Object.assign(URL, saved);
		}
	});

	it("dispositionName 优先 filename*，其次 filename", () => {
		expect(
			dispositionName(
				`attachment; filename="a.zip"; filename*=UTF-8''%E6%8A%A5%E5%91%8A.zip`,
			),
		).toBe("报告.zip");
		expect(dispositionName(`attachment; filename="a \\"b\\".zip"`)).toBe(
			'a "b".zip',
		);
		expect(dispositionName(null)).toBeNull();
	});
});
