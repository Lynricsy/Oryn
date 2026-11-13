/** 两步确认的按钮（撤销分享、撤回文件）：第一次点变成危险色的「确认…」，失焦复原。 */
import { type ReactNode, useState } from "react";
import { Button } from "../ui/primitives";

export function ConfirmButton({
	label,
	confirmLabel,
	disabled,
	onConfirm,
}: {
	label: string;
	confirmLabel: string;
	disabled?: boolean;
	onConfirm: () => void;
}): ReactNode {
	const [confirming, setConfirming] = useState(false);
	return (
		<Button
			size="sm"
			variant={confirming ? "danger" : "ghost"}
			disabled={disabled}
			onBlur={() => setConfirming(false)}
			onClick={() => {
				if (!confirming) return setConfirming(true);
				setConfirming(false);
				onConfirm();
			}}
		>
			{confirming ? confirmLabel : label}
		</Button>
	);
}
