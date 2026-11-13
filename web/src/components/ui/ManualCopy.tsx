/**
 * 复制失败时的出口：一句原因与下一步，加上可选中的完整文本与「再试一次」的复制按钮。
 * 用在「已发布 / 已创建 / 已生成，但没复制成功」的结果里（前一步本身是成功的，不改成错误态）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CopyButton } from "./CopyButton";
import { Icon } from "./icons";
import { TextInput } from "./primitives";

export function ManualCopy({
	text,
	label,
	message,
}: {
	text: string;
	/** 文本框的读屏名 */
	label: string;
	/** 为什么没复制、接下来怎么做 */
	message: string;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div role="alert" className="flex flex-col gap-1.5 text-xs">
			<p className="flex items-start gap-1.5 text-warn">
				<Icon
					name="alert"
					size={12}
					className="mt-[2px] shrink-0"
					aria-hidden
				/>
				{message}
			</p>
			<div className="flex min-w-0 items-center gap-1">
				<TextInput
					readOnly
					value={text}
					aria-label={label}
					className="min-w-0 flex-1 font-mono text-xs"
					onFocus={(e) => e.currentTarget.select()}
				/>
				<CopyButton text={text} label={t("common.copy")} />
			</div>
		</div>
	);
}
