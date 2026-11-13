/**
 * 写剪贴板并报告结果：Clipboard API 不可用（非安全上下文，如普通 HTTP）或被拒绝（权限、用户激活已失效）时为 false。
 * 调用方据此区分「已复制」与「复制失败，需要手动复制」，不把发出请求当成成功。
 */
export async function copyToClipboard(text: string): Promise<boolean> {
	if (typeof navigator === "undefined" || !navigator.clipboard) return false;
	try {
		await navigator.clipboard.writeText(text);
		return true;
	} catch {
		return false;
	}
}
