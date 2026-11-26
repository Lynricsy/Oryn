/**
 * 消息附件：任意文件都被接受并上传、超过 100MiB 在客户端拒绝、内联预算用完的图片只作为文件（「仅文件」）、
 * 上传未完成时 uploading 为真、移除已上传的附件会删除服务端副本；
 * 分享进来的已上传附件直接成为完成条目（addUploaded）；触屏上附件入口多一个拍照键；
 * 助手文件库模式（M13）：文件传进库、内联图片不上传，换去向时按新去向重传并删掉旧上传。
 */
import {
	act,
	fireEvent,
	render,
	renderHook,
	screen,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type * as AttachmentsLib from "../../lib/attachments";
import type { AssistantFile, Attachment } from "../../lib/attachments";
import type * as ImagesLib from "../../lib/images";
import { ImageBudgetError, type PreparedImage } from "../../lib/images";
import { AttachButton, AttachmentStrip, useAttachments } from "./attachments";
import { FileDropOverlay, useFileDrop } from "./FileDrop";

const { upload, del, uploadLib, delLib, prepare } = vi.hoisted(() => ({
	upload: vi.fn(),
	del: vi.fn(),
	uploadLib: vi.fn(),
	delLib: vi.fn(),
	prepare: vi.fn(),
}));
vi.mock("../../lib/attachments", async (orig) => ({
	...(await orig<typeof AttachmentsLib>()),
	uploadAttachment: upload,
	deleteAttachment: del,
	uploadAssistantFile: uploadLib,
	deleteAssistantFile: delLib,
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

	it("图片加入后在压缩预处理期间立即显现为 preparing 并阻止发送，完成后释放预览 URL", async () => {
		const decoding = deferred<PreparedImage>();
		prepare.mockReturnValueOnce(decoding.promise);
		const createUrl = vi
			.spyOn(URL, "createObjectURL")
			.mockReturnValue("blob:preview-1");
		const revokeUrl = vi
			.spyOn(URL, "revokeObjectURL")
			.mockImplementation(() => undefined);
		try {
			const { result } = renderHook(() =>
				useAttachments({ threadId: "T-1", library: true }),
			);
			const png = new File(["img"], "shot.png", { type: "image/png" });
			let adding!: Promise<void>;
			act(() => {
				adding = result.current.addFiles([png]);
			});
			expect(result.current.items).toHaveLength(1);
			expect(result.current.items[0]).toMatchObject({
				name: "shot.png",
				status: "preparing",
				previewUrl: "blob:preview-1",
			});
			expect(result.current.uploading).toBe(true);
			const { rerender } = render(
				<AttachmentStrip items={result.current.items} onRemove={() => {}} />,
			);
			expect(screen.getByText(i18n.t("attachments.preparing"))).toBeTruthy();
			expect(screen.getByAltText("shot.png")).toHaveAttribute(
				"src",
				"blob:preview-1",
			);
			await act(async () => {
				decoding.resolve(img);
				await adding;
			});
			expect(result.current.items[0]?.status).toBe("done");
			expect(result.current.items[0]?.previewUrl).toBeUndefined();
			expect(revokeUrl).toHaveBeenCalledWith("blob:preview-1");
			expect(result.current.uploading).toBe(false);
			rerender(
				<AttachmentStrip items={result.current.items} onRemove={() => {}} />,
			);
			expect(screen.queryByText(i18n.t("attachments.preparing"))).toBeNull();
		} finally {
			createUrl.mockRestore();
			revokeUrl.mockRestore();
		}
	});
});

describe("addUploaded（系统分享）", () => {
	it("已上传的附件直接成为完成条目：不再上传、不做内联副本，图片标「仅文件」", () => {
		const { result } = renderHook(() => useAttachments());
		const photo = { ...meta("att_p", "p.jpg"), mime: "image/jpeg" };
		act(() => result.current.addUploaded([meta("att_d", "d.pdf"), photo]));
		expect(upload).not.toHaveBeenCalled();
		expect(prepare).not.toHaveBeenCalled();
		expect(result.current.items).toMatchObject([
			{ name: "d.pdf", size: 3, status: "done", progress: 1, fileOnly: false },
			{ name: "p.jpg", status: "done", progress: 1, fileOnly: true },
		]);
		expect(result.current.attachmentIds).toEqual(["att_d", "att_p"]);
		expect(result.current.contents).toEqual([]);
		expect(result.current.uploading).toBe(false);
	});

	it("已上传与 fileOnly 的图片附件均显示小预览缩略图而非类型图标，点击打开灯箱", () => {
		const { result } = renderHook(() => useAttachments());
		const photo = { ...meta("att_p", "photo.jpg"), mime: "image/jpeg" };
		act(() => result.current.addUploaded([meta("att_d", "doc.pdf"), photo]));
		const { container } = render(
			<AttachmentStrip items={result.current.items} onRemove={() => {}} />,
		);
		// 图片附件展示小预览 img，不渲染文件类型图标；PDF 附件继续展示类型图标
		const thumb = screen.getByRole("img", { name: "photo.jpg" });
		expect(thumb).toHaveAttribute(
			"src",
			"/api/v1/attachments/att_p/content?inline=true",
		);
		expect(
			container.querySelectorAll('svg[data-file-icon="pdf"]'),
		).toHaveLength(1);
		expect(container.querySelector('svg[data-file-icon="image"]')).toBeNull();
		// 鼠标点击图片附件（含文字区）弹出灯箱预览
		fireEvent.click(screen.getByText("photo.jpg"));
		const dialog = screen.getByRole("dialog", { name: "photo.jpg" });
		expect(dialog.querySelector("img")?.getAttribute("src")).toBe(
			"/api/v1/attachments/att_p/content?inline=true",
		);
	});

	it("超过每条消息的上限后停止追加并报错", () => {
		const { result } = renderHook(() => useAttachments());
		const list = Array.from({ length: 22 }, (_, i) => meta(`att_${i}`, `${i}`));
		act(() => result.current.addUploaded(list));
		expect(result.current.items).toHaveLength(20);
		expect(result.current.error).toBe(
			i18n.t("attachments.tooMany", { max: 20 }),
		);
	});
});

describe("AttachButton", () => {
	/** 假 matchMedia：只回答 `(pointer: coarse)`。 */
	function pointer(coarse: boolean) {
		vi.stubGlobal("matchMedia", (q: string) => ({
			matches: coarse && q === "(pointer: coarse)",
			media: q,
			addEventListener: () => undefined,
			removeEventListener: () => undefined,
		}));
	}

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("细指针：只有回形针，没有拍照键", () => {
		pointer(false);
		render(<AttachButton onFiles={() => undefined} />);
		expect(screen.getByTestId("attach-file-input")).toHaveAttribute("multiple");
		expect(screen.queryByTestId("attach-camera")).toBeNull();
		expect(screen.queryByTestId("attach-camera-input")).toBeNull();
	});

	it("粗指针：多一个拍照键，直接调后置相机；选中后交出文件并清空 value", () => {
		pointer(true);
		const onFiles = vi.fn();
		render(<AttachButton onFiles={onFiles} />);
		expect(screen.getByTestId("attach-camera")).toHaveTextContent(
			i18n.t("attachments.camera"),
		);
		const cam = screen.getByTestId("attach-camera-input") as HTMLInputElement;
		expect(cam).toHaveAttribute("type", "file");
		expect(cam).toHaveAttribute("accept", "image/*");
		expect(cam).toHaveAttribute("capture", "environment");
		const photo = new File(["x"], "photo.jpg", { type: "image/jpeg" });
		fireEvent.change(cam, { target: { files: [photo] } });
		expect(onFiles).toHaveBeenCalledWith([photo]);
		expect(cam.value).toBe("");
	});
});

describe("useAttachments 助手文件库模式", () => {
	const libFile = (id: string, name: string): AssistantFile => ({
		id,
		name,
		mime: "text/plain",
		size: 3,
		sha256: "0".repeat(64),
		scope: "conversation",
		source: "upload",
		createdAt: 0,
	});

	beforeEach(() => {
		upload.mockReset();
		del.mockReset().mockResolvedValue(undefined);
		uploadLib.mockReset();
		delLib.mockReset().mockResolvedValue(undefined);
		prepare.mockReset();
	});

	it("文件传进助手文件库（fileIds），有内联副本的图片不上传、只随 images 发送", async () => {
		uploadLib.mockResolvedValue(libFile("afl_1", "a.txt"));
		prepare.mockResolvedValue(img);
		const { result } = renderHook(() =>
			useAttachments({ threadId: "T-k", library: true }),
		);
		const txt = new File(["abc"], "a.txt", { type: "text/plain" });
		const png = new File(["x"], "a.png", { type: "image/png" });
		await act(() => result.current.addFiles([txt, png]));
		expect(uploadLib).toHaveBeenCalledTimes(1);
		expect(uploadLib).toHaveBeenCalledWith(
			txt,
			expect.objectContaining({ threadId: "T-k" }),
		);
		expect(upload).not.toHaveBeenCalled();
		expect(result.current.fileIds).toEqual(["afl_1"]);
		expect(result.current.attachmentIds).toEqual([]);
		expect(result.current.contents).toHaveLength(1);
		expect(result.current.hasFiles).toBe(true);
	});

	it("换去向：已加入的文件按新去向重传，旧上传尽力删除；分享来的线程附件切到助手时标为错误", async () => {
		upload.mockResolvedValue(meta("att_1", "a.txt"));
		uploadLib.mockResolvedValue(libFile("afl_1", "a.txt"));
		const { result, rerender } = renderHook(
			({ library }) => useAttachments({ library }),
			{ initialProps: { library: false } },
		);
		const txt = new File(["abc"], "a.txt", { type: "text/plain" });
		await act(() => result.current.addFiles([txt]));
		act(() => result.current.addUploaded([meta("att_s", "shared.txt")]));
		expect(result.current.attachmentIds).toEqual(["att_1", "att_s"]);

		await act(async () => rerender({ library: true }));
		expect(del).toHaveBeenCalledWith("att_1");
		expect(uploadLib).toHaveBeenCalledWith(txt, expect.anything());
		expect(result.current.fileIds).toEqual(["afl_1"]);
		expect(result.current.attachmentIds).toEqual([]);
		expect(
			result.current.items.find((i) => i.name === "shared.txt"),
		).toMatchObject({ status: "error" });

		await act(async () => rerender({ library: false }));
		expect(delLib).toHaveBeenCalledWith("afl_1");
		expect(result.current.attachmentIds).toEqual(["att_1", "att_s"]);
		expect(result.current.fileIds).toEqual([]);
	});

	it("首图预处理期间切换发送对象：旧队列恢复后不删掉后排文件的新上传控制器，移除仍能取消上传", async () => {
		const decoding = deferred<PreparedImage>();
		const uploading = deferred<AssistantFile>();
		prepare.mockReturnValueOnce(decoding.promise);
		uploadLib.mockReturnValueOnce(uploading.promise);

		const { result, rerender } = renderHook(
			({ library }) => useAttachments({ library }),
			{ initialProps: { library: false } },
		);
		const png = new File(["a"], "a.png", { type: "image/png" });
		const txt = new File(["b"], "b.txt", { type: "text/plain" });
		let adding!: Promise<void>;
		act(() => {
			adding = result.current.addFiles([png, txt]);
		});

		// 首图仍在预处理时切到助手文件库：b.txt 立即按新去向发起上传并换上新控制器
		act(() => rerender({ library: true }));
		expect(uploadLib).toHaveBeenCalledTimes(1);
		const signal = uploadLib.mock.calls[0]?.[1]?.signal as
			| AbortSignal
			| undefined;
		expect(signal).toBeDefined();
		expect(signal?.aborted).toBe(false);

		// 首图预处理完成、旧 addFiles 循环继续扫过 b.txt，随后用户移除 b.txt：新上传必须能被中止
		await act(async () => {
			decoding.resolve(img);
			await adding;
		});
		const txtItem = result.current.items.find((i) => i.name === "b.txt");
		expect(txtItem).toBeDefined();
		act(() => result.current.remove(txtItem?.key ?? ""));
		expect(signal?.aborted).toBe(true);
	});
});

function DropHost({
	enabled = true,
	onFiles,
}: {
	enabled?: boolean;
	onFiles: (files: File[]) => void;
}) {
	const drop = useFileDrop({ enabled, onFiles });
	return (
		<div data-testid="drop-host" {...drop.bind}>
			<div data-testid="drop-child">body</div>
			<FileDropOverlay active={drop.dragging} />
		</div>
	);
}

describe("FileDrop", () => {
	it("拖入文件显示提示遮罩（子元素进出不提前隐藏），松手后隐藏并交出文件；非文件拖拽不激活", () => {
		const onFiles = vi.fn();
		render(<DropHost onFiles={onFiles} />);
		const host = screen.getByTestId("drop-host");
		const child = screen.getByTestId("drop-child");
		const file = new File(["abc"], "shot.png", { type: "image/png" });

		// 非文件拖拽不激活遮罩
		fireEvent.dragEnter(host, {
			dataTransfer: { types: ["text/plain"], files: [] },
		});
		expect(screen.queryByTestId("file-drop-overlay")).toBeNull();

		// 拖入文件激活遮罩，子元素切换不导致提前消失
		const dt = { types: ["Files"], files: [file], dropEffect: "none" };
		fireEvent.dragEnter(host, { dataTransfer: dt });
		expect(screen.getByTestId("file-drop-overlay")).toBeTruthy();
		expect(screen.getByText(i18n.t("attachments.dropHint"))).toBeTruthy();
		fireEvent.dragEnter(child, { dataTransfer: dt });
		fireEvent.dragLeave(host, { dataTransfer: dt });
		expect(screen.getByTestId("file-drop-overlay")).toBeTruthy();
		fireEvent.dragOver(host, { dataTransfer: dt });
		expect(dt.dropEffect).toBe("copy");

		// 松手投放：隐藏遮罩并回调文件
		fireEvent.drop(host, { dataTransfer: dt });
		expect(screen.queryByTestId("file-drop-overlay")).toBeNull();
		expect(onFiles).toHaveBeenCalledWith([file]);
	});
});
