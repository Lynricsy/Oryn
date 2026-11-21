/**
 * box 线程停机时代替「唤醒」的入口（M23 步骤 7.5）：box 的启停只由 Boxes 页决定，线程页不唤醒它。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { Icon } from "../ui/icons";

export function BoxStartLink({ boxId }: { boxId: string }): ReactNode {
	const { t } = useTranslation();
	return (
		<Link
			to={`/boxes/${boxId}`}
			data-testid="box-start-link"
			className="inline-flex h-7 items-center gap-1 rounded-md px-2.5 font-medium text-fg text-xs shadow-xs ring-1 ring-border transition-colors duration-150 hover:bg-hover"
		>
			<Icon name="hardDrives" size={13} />
			{t("boxes.startOnPage")}
		</Link>
	);
}
