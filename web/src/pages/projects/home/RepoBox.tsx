/**
 * 仓库条目的外框（仿 GitHub 的文件表）：圆角描边纸面，可选表头行（浅底、下发丝线）；行与行之间的发丝线由条目列表自己画。
 * 主页「文件」区块与文件页的目录视图共用。
 */
import type { ReactNode } from "react";

export function RepoBox({
	header,
	children,
}: {
	header?: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<section className="overflow-hidden rounded-lg border border-border bg-panel">
			{header && (
				<header className="flex min-h-12 items-center gap-2 border-border border-b bg-raised px-4 py-2 text-sm">
					{header}
				</header>
			)}
			{children}
		</section>
	);
}
