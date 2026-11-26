/**
 * 图片的上下文菜单组：复制图片（剪贴板只收 PNG，其它格式先转一次）、下载、引用到输入框（作为附件）。
 */
import i18n from "../../i18n";
import { enqueueComposerFiles } from "../../state/composer-drafts";
import { showToast } from "../../state/toasts";
import { uiStore } from "../../state/ui";
import type { MenuGroup } from "./types";

async function fetchBlob(src: string): Promise<Blob> {
	const res = await fetch(src, { credentials: "same-origin" });
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	return res.blob();
}

async function toPng(blob: Blob): Promise<Blob> {
	if (blob.type === "image/png") return blob;
	const bitmap = await createImageBitmap(blob);
	const canvas = document.createElement("canvas");
	canvas.width = bitmap.width;
	canvas.height = bitmap.height;
	canvas.getContext("2d")?.drawImage(bitmap, 0, 0);
	bitmap.close();
	return new Promise((resolve, reject) =>
		canvas.toBlob(
			(png) => (png ? resolve(png) : reject(new Error("toBlob failed"))),
			"image/png",
		),
	);
}

/** 扩展名：取自 blob 类型或 data: 头，缺省 png。 */
function extOf(src: string, type: string): string {
	const mime = type || /^data:([^;,]+)/.exec(src)?.[1] || "";
	const sub = mime.split("/")[1]?.split("+")[0];
	return sub && /^[a-z0-9]+$/.test(sub)
		? sub === "jpeg"
			? "jpg"
			: sub
		: "png";
}

function failed() {
	showToast({ text: i18n.t("contextMenu.copyFailed"), tone: "err" });
}

export function canCopyImage(): boolean {
	return (
		typeof ClipboardItem !== "undefined" &&
		typeof navigator.clipboard?.write === "function"
	);
}

/** 把图片源转成 PNG 写入剪贴板；失败抛出异常由调用方提示。 */
export async function copyImageToClipboard(src: string): Promise<void> {
	const png = await fetchBlob(src).then(toPng);
	await navigator.clipboard.write([new ClipboardItem({ "image/png": png })]);
}

/** 触发浏览器下载图片；未提供文件名时按时间戳与推断扩展名生成。 */
export function downloadImage(src: string, filename?: string): void {
	const type = /^data:([^;,]+)/.exec(src)?.[1] ?? "";
	const a = document.createElement("a");
	a.href = src;
	a.download = filename?.trim() || `image-${Date.now()}.${extOf(src, type)}`;
	document.body.append(a);
	a.click();
	a.remove();
}

export function imageGroup(src: string, askThreadId: string | null): MenuGroup {
	const items: MenuGroup["items"] = [];
	if (canCopyImage())
		items.push({
			key: "copyImage",
			icon: "image",
			label: i18n.t("contextMenu.copyImage"),
			onSelect: () => void copyImageToClipboard(src).catch(failed),
		});
	items.push({
		key: "download",
		icon: "download",
		label: i18n.t("contextMenu.downloadImage"),
		onSelect: () => downloadImage(src),
	});
	if (askThreadId)
		items.push({
			key: "attach",
			icon: "attach",
			label: i18n.t("contextMenu.insert"),
			onSelect: () =>
				void fetchBlob(src).then(
					(blob) => {
						const file = new File(
							[blob],
							`image-${Date.now()}.${extOf(src, blob.type)}`,
							{ type: blob.type || "image/png" },
						);
						enqueueComposerFiles(askThreadId, [file]);
						uiStore.getState().setThreadTab("agent");
					},
					(e: unknown) =>
						showToast({
							text: e instanceof Error ? e.message : String(e),
							tone: "err",
						}),
				),
		});
	return { key: "image", items };
}
