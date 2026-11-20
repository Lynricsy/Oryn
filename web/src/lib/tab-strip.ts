/**
 * 横向标签条：把选中的标签滚进条内可视范围，只动标签条自己的 scrollLeft。
 * 不用 `scrollIntoView`：它会连带滚动所有可滚动的祖先（页面主滚动区、甚至 overflow:hidden 的外壳），
 * 嵌在长页面里的标签条一挂载就把整页拉走。
 */
export function revealSelectedTab(strip: HTMLElement): void {
	const tab = strip.querySelector<HTMLElement>("[aria-selected=true]");
	if (!tab) return;
	const s = strip.getBoundingClientRect();
	const r = tab.getBoundingClientRect();
	if (r.left < s.left) strip.scrollLeft -= s.left - r.left;
	else if (r.right > s.right) strip.scrollLeft += r.right - s.right;
}
