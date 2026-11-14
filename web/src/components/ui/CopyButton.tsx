/** 复制到剪贴板按钮。 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "./icons";

export function CopyButton({
	text,
	label,
	ariaLabel,
	testId,
}: {
	text: string;
	/** 可见文字（同时作读屏名） */
	label?: string;
	/** 只给读屏与悬停提示的名字（无可见文字时用） */
	ariaLabel?: string;
	testId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const [done, setDone] = useState(false);
	return (
		<button
			type="button"
			data-testid={testId}
			className={`inline-flex h-6 items-center gap-1 rounded-sm px-1.5 text-xs transition-colors duration-150 hover:bg-hover ${done ? "text-ok" : "text-faint hover:text-fg"}`}
			onClick={() => {
				void navigator.clipboard?.writeText(text).then(() => {
					setDone(true);
					setTimeout(() => setDone(false), 1200);
				});
			}}
			aria-label={ariaLabel ?? label ?? t("common.copy")}
			title={label ? undefined : (ariaLabel ?? t("common.copy"))}
		>
			<Icon
				name={done ? "check" : "copy"}
				size={13}
				weight={done ? "bold" : "regular"}
				aria-hidden
			/>
			{label && <span>{done ? t("common.copied") : label}</span>}
		</button>
	);
}
