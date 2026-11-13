/**
 * 消息附件：任意文件都被接受并上传、超过 100MiB 在客户端拒绝、内联预算用完的图片只作为文件（「仅文件」）、
 * 上传未完成时 uploading 为真、移除已上传的附件会删除服务端副本。
 */
import { act, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type * as AttachmentsLib from "../../lib/attachments";
import type { Attachment } from "../../lib/attachments";
import type * as ImagesLib from "../../lib/images";
import { ImageBudgetError, type PreparedImage } from "../../lib/images";
import { AttachmentStrip, useAttachments } from "./attachments";

const { upload, del, prepare } = vi.hoisted(() => ({
	upload: vi.fn(),
	del: vi.fn(),
	prepare: vi.fn(),
}));
vi.mock("../../lib/attachments", async (orig) => ({
	...(await orig<typeof AttachmentsLib>()),
	uploadAttachment: upload,
	deleteAttachment: del,
}));
vi.mock("../../lib/images", async (orig) => ({
	...(await orig<typeof ImagesLib>()),
	prepareImage: prepare,
}));

/** 手动控制的上传结果（tsconfig 的 lib 不含 Promise.withResolvers）。 */
function deferred<T>() {
	let resolve!: (v: T) => void;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

function meta(id: string, name: string): Attachment {
	return {
		id,
		name,
		mime: "application/octet-stream",
		size: 3,
		sha256: "0".repeat(64),
		createdAt: 0,
	};
}

const img: PreparedImage = {
	type: "image",
	data: "AAAA",
	mimeType: "image/webp",
	width: 1,
	height: 1,
	inlineBytes: 4,
	name: "a.png",
};

describe("useAttachments", () => {
	beforeEach(() => {
		upload.mockReset();
		del.mockReset().mockResolvedValue(undefined);
		prepare.mockReset();
	});

	it("接受任意文件：加入即上传，完成前 uploading，完成后带上 id", async () => {
		const d = deferred<Attachment>();
		upload.mockReturnValue(d.promise);
		const { result } = renderHook(() => useAttachments({ threadId: "T-1" }));
		const zip = new File(["abc"], "src.zip", { type: "application/zip" });
		await act(() => result.current.addFiles([zip]));
		expect(upload).toHaveBeenCalledWith(
			zip,
			expect.objectContaining({ threadId: "T-1" }),
		);
		expect(result.current.uploading).toBe(true);
		expect(result.current.attachmentIds).toEqual([]);
		await act(async () => d.resolve(meta("att_1", "src.zip")));
		expect(result.current.uploading).toBe(false);
		expect(result.current.attachmentIds).toEqual(["att_1"]);
		expect(result.current.contents).toEqual([]);
		expect(prepare).not.toHaveBeenCalled();
	});

	it("超过 100MiB 的文件在客户端被拒，不上传", async () => {
		const { result } = renderHook(() => useAttachments());
		const big = new File(["x"], "big.bin");
		Object.defineProperty(big, "size", { value: 100 * 1024 * 1024 + 1 });
		await act(() => result.current.addFiles([big]));
		expect(upload).not.toHaveBeenCalled();
		expect(result.current.items).toHaveLength(1);
		expect(result.current.items[0]?.status).toBe("error");
		expect(result.current.attachmentIds).toEqual([]);
		expect(result.current.uploading).toBe(false);
	});

	it("内联预算用完的图片只作为文件附件并标「仅文件」", async () => {
		upload
			.mockResolvedValueOnce(meta("att_a", "a.png"))
			.mockResolvedValueOnce(meta("att_b", "b.png"));
		prepare
			.mockResolvedValueOnce(img)
			.mockRejectedValueOnce(new ImageBudgetError(900_000, 10));
		const { result } = renderHook(() => useAttachments());
		await act(() =>
			result.current.addFiles([
				new File(["a"], "a.png", { type: "image/png" }),
				new File(["b"], "b.png", { type: "image/png" }),
			]),
		);
		expect(result.current.attachmentIds).toEqual(["att_a", "att_b"]);
		expect(result.current.contents).toEqual([
			{ type: "image", data: "AAAA", mimeType: "image/webp" },
		]);
		expect(result.current.items.map((i) => i.fileOnly ?? false)).toEqual([
			false,
			true,
		]);
		render(
			<AttachmentStrip items={result.current.items} onRemove={() => {}} />,
		);
		expect(
			screen.getByText(new RegExp(i18n.t("attachments.fileOnly"))),
		).toBeTruthy();
	});

	it("上传失败显示在该文件上，不随消息发送；移除已上传的附件删除服务端副本", async () => {
		upload
			.mockRejectedValueOnce(new Error("boom"))
			.mockResolvedValueOnce(meta("att_ok", "ok.txt"));
		const { result } = renderHook(() => useAttachments());
		await act(() =>
			result.current.addFiles([
				new File(["x"], "bad.txt"),
				new File(["y"], "ok.txt"),
			]),
		);
		expect(result.current.items.map((i) => i.status)).toEqual([
			"error",
			"done",
		]);
		expect(result.current.attachmentIds).toEqual(["att_ok"]);
		const ok = result.current.items[1];
		act(() => result.current.remove(ok?.key ?? ""));
		expect(del).toHaveBeenCalledWith("att_ok");
		expect(result.current.attachmentIds).toEqual([]);
	});

	it("图片预处理期间 composer 卸载（换线程）：预处理完成后不加入、不上传，后续文件也不处理", async () => {
		const decoding = deferred<PreparedImage>();
		prepare.mockReturnValueOnce(decoding.promise);
		upload.mockResolvedValue(meta("att_x", "x"));
		const { result, unmount } = renderHook(() =>
			useAttachments({ threadId: "T-A" }),
		);
		let adding!: Promise<void>;
		act(() => {
			adding = result.current.addFiles([
				new File(["a"], "a.png", { type: "image/png" }),
				new File(["b"], "b.txt"),
			]);
		});
		expect(prepare).toHaveBeenCalledTimes(1);
		unmount();
		decoding.resolve(img);
		await adding;
		expect(upload).not.toHaveBeenCalled();
	});
});
