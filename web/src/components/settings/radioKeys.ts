/** 单选组的方向键处理（外观设置的主题、App 图标等单选组共用）。 */
import type { KeyboardEvent } from "react";

/** 单选组的方向键：在组内的 `role=radio` 之间移动焦点并选中。 */
export function radioKeys(e: KeyboardEvent<HTMLElement>) {
	const step: Record<string, number> = {
		ArrowRight: 1,
		ArrowDown: 1,
		ArrowLeft: -1,
		ArrowUp: -1,
	};
	const d = step[e.key];
	if (!d) return;
	const radios = Array.from(
		e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]'),
	);
	const i = radios.indexOf(document.activeElement as HTMLButtonElement);
	if (i < 0) return;
	e.preventDefault();
	const next = radios[(i + d + radios.length) % radios.length];
	next?.focus();
	next?.click();
}
