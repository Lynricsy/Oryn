/** 字形选择网格（项目图标与各种头像共用）与单选组的方向键处理。 */
import type { KeyboardEvent, ReactNode } from "react";
import type { Glyph } from "../../lib/glyphs";

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

export function GlyphGrid({
	glyphs,
	value,
	label,
	disabled,
	onPick,
}: {
	glyphs: readonly Glyph[];
	value?: string;
	label: string;
	disabled?: boolean;
	onPick: (char: string) => void;
}): ReactNode {
	return (
		<fieldset
			aria-label={label}
			className="m-0 grid min-w-0 grid-cols-[repeat(auto-fill,minmax(2rem,1fr))] gap-1 border-0 p-0"
		>
			{glyphs.map((g) => {
				const on = g.char === value;
				return (
					<button
						key={g.char}
						type="button"
						title={g.label}
						aria-label={g.label}
						aria-pressed={on}
						disabled={disabled}
						onClick={() => onPick(g.char)}
						className={`grid h-8 place-items-center rounded-md border text-lg leading-none transition-colors duration-150 disabled:opacity-50 ${on ? "border-accent bg-accent-soft text-accent-ink" : "border-transparent text-muted hover:bg-hover hover:text-fg"}`}
					>
						{g.char}
					</button>
				);
			})}
		</fieldset>
	);
}
