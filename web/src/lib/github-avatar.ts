/**
 * 由提交邮箱推出 GitHub 头像地址（不调 API）：只认 GitHub 的 noreply 邮箱——
 * `<id>+<login>@users.noreply.github.com`（2017 年后）用数字 id，`<login>@users.noreply.github.com`（旧式）用登录名。
 * 其它邮箱推不出账号，返回 undefined，调用方显示首字头像。
 */

const NOREPLY =
	/^(?:([0-9]+)\+)?([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\[bot\])?)@users\.noreply\.github\.com$/i;

/** `px`：要的边长（像素，调用方按设备像素比放大）。 */
export function githubAvatarUrl(email: string, px: number): string | undefined {
	const m = NOREPLY.exec(email.trim());
	if (!m) return undefined;
	const [, id, login = ""] = m;
	const s = Math.max(1, Math.round(px));
	return id
		? `https://avatars.githubusercontent.com/u/${id}?s=${s}&v=4`
		: `https://github.com/${encodeURIComponent(login)}.png?size=${s}`;
}
