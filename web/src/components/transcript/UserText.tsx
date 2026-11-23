/**
 * 用户气泡正文：轻量 Markdown（切分见 `lib/user-markdown`）。每个字符都原样显示，语法记号只是变淡，
 * 便于核对输入、选中复制得到的就是原文；只有粗体、代码、引用、链接等的样式与纯文本不同。
 * 真实消息与「发送中」的待发任务共用，到达时不跳。
 */
import { type ReactNode, useMemo } from "react";
import { type UserMdStyle, userMarkdown } from "../../lib/user-markdown";

/** 用户气泡正文排版：保留原文的空白与换行。 */
const USER_BUBBLE_TEXT =
	"whitespace-pre-wrap break-words text-[0.875rem] leading-[1.375rem]";

/** 片段样式；颜色只取一个（Tailwind 同类工具类按样式表先后生效，不能叠）：记号 > 链接 > 引用。 */
function segmentClass(s: UserMdStyle): string {
	const c: string[] = [];
	if (s.block) c.push("font-mono text-[0.86em]");
	if (s.code) c.push("bg-inset font-mono text-[0.9em]");
	if (s.heading || s.strong) c.push("font-semibold");
	if (s.em) c.push("italic");
	if (s.del) c.push("line-through");
	if (s.link) c.push("underline decoration-accent/40 underline-offset-2");
	if (s.marker) c.push("text-faint");
	else if (s.link) c.push("text-accent");
	else if (s.quote) c.push("text-muted");
	return c.join(" ");
}

export function UserText({ text }: { text: string }): ReactNode {
	const segments = useMemo(() => userMarkdown(text), [text]);
	return (
		<div className={USER_BUBBLE_TEXT}>
			{segments.map((seg, i) => {
				const cls = segmentClass(seg.style);
				if (seg.href)
					return (
						<a
							// biome-ignore lint/suspicious/noArrayIndexKey: 片段由原文切出，顺序即身份
							key={i}
							href={seg.href}
							target="_blank"
							rel="noopener noreferrer"
							className={cls}
						>
							{seg.text}
						</a>
					);
				return cls ? (
					// biome-ignore lint/suspicious/noArrayIndexKey: 片段由原文切出，顺序即身份
					<span key={i} className={cls}>
						{seg.text}
					</span>
				) : (
					seg.text
				);
			})}
		</div>
	);
}
