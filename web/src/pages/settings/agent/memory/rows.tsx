/**
 * 记忆分组里的「条目行」：内容型的列表项（事实、来源、记忆文档、主题）——正文可能多行、下方还有元信息与就地表单，
 * 放不进 `GroupRow` 的「标题 + 一句说明 + 行尾」。与 `GroupRow` 同在 `GroupList` 里，内边距与悬停底色一致；
 * 可带 `data-*` 定位属性。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../../../../components/ui/primitives";

export function ItemRow({
	children,
	...data
}: {
	children: ReactNode;
	[data: `data-${string}`]: string | undefined;
}): ReactNode {
	return (
		<li
			{...data}
			className="flex min-w-0 flex-col gap-1.5 py-3 pr-3 pl-3.5 transition-colors duration-150 hover:bg-raised"
		>
			{children}
		</li>
	);
}

/** 条目行下方的元信息行：次要灰小字，可换行；`actions` 靠右（手机上换到下一行）。 */
export function ItemMeta({
	children,
	actions,
}: {
	children?: ReactNode;
	actions?: ReactNode;
}): ReactNode {
	return (
		<div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12.5px] text-muted">
			{children}
			{actions && (
				<span className="ml-auto flex shrink-0 flex-wrap items-center gap-1">
					{actions}
				</span>
			)}
		</div>
	);
}

/**
 * 分页条：上一页 / 下一页，靠右；给了 `shown`（本页条数）时前面加「第 a–b 条」。
 * 只有一页时不渲染。
 */
export function Pager({
	offset,
	page,
	hasMore,
	shown,
	onOffset,
}: {
	offset: number;
	page: number;
	hasMore: boolean | undefined;
	shown?: number;
	onOffset: (offset: number) => void;
}): ReactNode {
	const { t } = useTranslation();
	if (offset === 0 && !hasMore) return null;
	return (
		<div className="flex items-center justify-end gap-2 px-1.5">
			{shown !== undefined && (
				<span className="text-faint text-xs tabular-nums">
					{t("memoryPage.browse.range", {
						from: offset + 1,
						to: offset + shown,
					})}
				</span>
			)}
			<Button
				size="sm"
				icon="chevronLeft"
				disabled={offset === 0}
				onClick={() => onOffset(Math.max(0, offset - page))}
			>
				{t("memoryPage.browse.prev")}
			</Button>
			<Button
				size="sm"
				disabled={!hasMore}
				onClick={() => onOffset(offset + page)}
			>
				{t("memoryPage.browse.next")}
			</Button>
		</div>
	);
}
