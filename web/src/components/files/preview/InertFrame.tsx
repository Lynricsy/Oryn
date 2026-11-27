/**
 * 惰性 iframe：显示不可信的 HTML（HTML 文件、Notebook 的 HTML 输出）。
 * - `sandbox="allow-same-origin"` 且不给 `allow-scripts`：帧内脚本、内联事件与 `javascript:` 链接一律不执行，
 *   表单、弹窗与顶层导航也被禁止；同源只为让父页面读取帧内文档（随内容测高度）。
 * - 文档开头注入 `<meta>` CSP：只放行 `data:` / `blob:` 资源与内联样式，帧内内容不能向任何地址发请求
 *   （相对地址会解析到主站，带着 Cookie 的 GET 也不行）。
 */
import { type ReactNode, useEffect, useRef } from "react";

const FRAME_CSP =
	"default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data: blob:; media-src data: blob:";

const META = `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}">`;

/** 在 HTML 文本最前面（`<!doctype>` 之后）插入 CSP `<meta>`；它必须先于任何能发请求的元素被解析。 */
export function lockdownHtml(html: string): string {
	const m = /^\s*(<!doctype[^>]*>)/i.exec(html);
	if (!m) return META + html;
	return m[1] + META + html.slice(m[0].length);
}

export function InertFrame({
	html,
	title,
	autoHeight = false,
	className = "",
}: {
	html: string;
	title: string;
	/** 高度随内容（Notebook 输出）；否则填满父容器 */
	autoHeight?: boolean;
	className?: string;
}): ReactNode {
	const ref = useRef<HTMLIFrameElement>(null);

	useEffect(() => {
		const frame = ref.current;
		if (!frame) return;
		let observer: ResizeObserver | undefined;
		const handle = () => {
			observer?.disconnect();
			const doc = frame.contentDocument;
			if (!doc) return;
			if (autoHeight) {
				const fitHeight = () => {
					frame.style.height = `${doc.documentElement.scrollHeight}px`;
				};
				fitHeight();
				observer = new ResizeObserver(fitHeight);
				observer.observe(doc.body);
			}
		};
		frame.addEventListener("load", handle);
		// 即使值没变，重新设置 srcdoc 也会重新装载
		frame.srcdoc = lockdownHtml(html);
		return () => {
			frame.removeEventListener("load", handle);
			observer?.disconnect();
		};
	}, [html, autoHeight]);

	return (
		<iframe
			ref={ref}
			title={title}
			sandbox="allow-same-origin"
			referrerPolicy="no-referrer"
			className={`block w-full border-0 bg-white ${autoHeight ? "" : "h-full"} ${className}`}
		/>
	);
}
