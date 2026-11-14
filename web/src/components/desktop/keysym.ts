/** X11 keysym（软键盘与附加键用）：Latin-1 可见字符取码点本身，其余 Unicode 字符取 `0x01000000 + 码点`。 */

export const KEYSYM = {
	BackSpace: 0xff08,
	Tab: 0xff09,
	Return: 0xff0d,
	Escape: 0xff1b,
	Control_L: 0xffe3,
	Alt_L: 0xffe9,
	Left: 0xff51,
	Up: 0xff52,
	Right: 0xff53,
	Down: 0xff54,
} as const;

/** 单个字符（一个码点）的 keysym；换行映射为 Return、制表符映射为 Tab。 */
export function keysymOf(char: string): number {
	const cp = char.codePointAt(0) ?? 0;
	if (cp === 0x0a || cp === 0x0d) return KEYSYM.Return;
	if (cp === 0x09) return KEYSYM.Tab;
	if ((cp >= 0x20 && cp <= 0x7e) || (cp >= 0xa0 && cp <= 0xff)) return cp;
	return 0x01000000 + cp;
}
