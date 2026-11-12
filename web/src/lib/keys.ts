/**
 * 为没有稳定 id、且只整体替换不重排的静态列表（表单表格、报告列表等）生成 React key。
 * 不要用于可重排 / 可编辑的列表。
 */
export function keyed<T>(
	items: readonly T[],
	prefix = "k",
): { key: string; item: T }[] {
	return items.map((item, i) => ({ key: `${prefix}${i}`, item }));
}
