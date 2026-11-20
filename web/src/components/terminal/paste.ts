/**
 * 读系统剪贴板粘进终端：xterm 有 `paste` 时走括号粘贴，否则直接作为输入发出。
 * 终端按键条的「粘贴」与终端右键菜单共用。
 */
import { terminalHub } from "../../state/terminals";

export async function pasteIntoTerminal(
	threadId: string,
	terminalId: string,
): Promise<void> {
	try {
		const text = await navigator.clipboard.readText();
		const term = terminalHub.session(threadId, terminalId)?.term;
		if (term?.paste) term.paste(text);
		else terminalHub.input(threadId, terminalId, text);
	} catch {
		// 未授权读取剪贴板
	}
}
