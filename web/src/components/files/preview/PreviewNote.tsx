/** 预览区顶部的一行提示（截断、降级显示等），与 FileViewer 的「只读显示前 N」提示同样式。 */
import type { ReactNode } from "react";
import { Icon } from "../../ui/icons";

export function PreviewNote({ children }: { children: ReactNode }): ReactNode {
	return (
		<p className="flex shrink-0 items-center gap-1.5 bg-warn/8 px-3 py-1.5 text-warn text-xs">
			<Icon name="info" size={13} className="shrink-0" />
			{children}
		</p>
	);
}
