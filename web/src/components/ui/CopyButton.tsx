/** 复制到剪贴板按钮。 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "./icons";

export function CopyButton({
	text,
	label,
}: {
	text: string;
	label?: string;
}): ReactNode {
	const { t } = useTranslation();
	const [done, setDone] = useState(false);
	return (
		<button
			type="button"
			className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-faint text-xs hover:bg-hover hover:text-fg"
			onClick={() => {
				void navigator.clipboard?.writeText(text).then(() => {
					setDone(true);
					setTimeout(() => setDone(false), 1200);
				});
			}}
			aria-label={label ?? t("common.copy")}
		>
			<Icon name={done ? "check" : "copy"} size={13} />
			{label && <span>{done ? t("common.copied") : label}</span>}
		</button>
	);
}
