/**
 * artifact 的下载（计划 M14 2.7）：一律走 `<a download>`，不用 `window.open`（移动壳里 `window.open` 交给系统浏览器，
 * 同源 `blob:` 的 `download` 链接则走原生下载）。
 */

/** 用一个临时的 `<a href download>` 下载服务端地址（同 `FileBrowser` 的写法）。 */
export function downloadUrl(url: string, name: string): void {
	const a = document.createElement("a");
	a.href = url;
	a.download = name;
	document.body.appendChild(a);
	a.click();
	a.remove();
}

/** 下载内存里的内容：同源 `blob:` 地址，点完之后释放。 */
export function downloadBlob(blob: Blob, name: string): void {
	const url = URL.createObjectURL(blob);
	try {
		downloadUrl(url, name);
	} finally {
		// 点击已同步发起下载；稍后再释放，给浏览器取走内容的时间
		setTimeout(() => URL.revokeObjectURL(url), 60_000);
	}
}

/**
 * `Content-Disposition` 里的文件名：优先 `filename*=UTF-8''…`（RFC 5987），其次 `filename="…"`；都没有回 `null`。
 */
export function dispositionName(header: string | null): string | null {
	if (!header) return null;
	const ext = /filename\*\s*=\s*([^']*)'[^']*'([^;]+)/i.exec(header);
	if (ext?.[2]) {
		try {
			return decodeURIComponent(ext[2].trim());
		} catch {
			// 编码坏了就退到普通的 filename
		}
	}
	const plain =
		/filename\s*=\s*"((?:[^"\\]|\\.)*)"|filename\s*=\s*([^;]+)/i.exec(header);
	const name = plain?.[1]?.replace(/\\(.)/g, "$1") ?? plain?.[2]?.trim();
	return name || null;
}
