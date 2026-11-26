/** 设置各分区共用的小部件：卡片内列表与列表行前置图标。 */
import type { ReactNode } from "react";
import { Icon, type IconName } from "../../components/ui/icons";

/** 卡片内的列表：行自带内边距，向两侧各收 2 个单位与卡片标题对齐。 */
export function RowList({ children }: { children: ReactNode }) {
	return <ul className="-mx-2 flex flex-col">{children}</ul>;
}

/** 列表行前置的细描边图标方块。 */
export function LeadIcon({ name }: { name: IconName }) {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name={name} size={15} />
		</span>
	);
}
