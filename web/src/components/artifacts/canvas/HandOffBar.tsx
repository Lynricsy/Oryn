/**
 * 「交给 Agent（已选 N 项）」条（计划 M14 6.6）：画布上有选中时浮在画布区右上角。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "../../ui/primitives";

export function HandOffBar({
	count,
	busy,
	onHandOff,
}: {
	count: number;
	busy: boolean;
	onHandOff(): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="pointer-events-none absolute top-2 right-2 z-10 flex">
			<Button
				size="sm"
				icon="sparkle"
				data-testid="canvas-handoff"
				className="pointer-events-auto shadow-sm"
				disabled={busy}
				onClick={onHandOff}
			>
				{t("artifacts.canvas.handOff.bar", { n: count })}
			</Button>
		</div>
	);
}
