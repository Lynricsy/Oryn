import { host } from "./host";

/** 当前宿主的「更换服务器」：桌面壳与移动壳各自实现，纯浏览器没有（浏览器里改为退出登录）。 */
export function changeServerFn(): (() => void) | null {
	const { desktop, mobile } = host;
	if (desktop) return () => desktop.changeServer();
	if (mobile) return () => mobile.changeServer();
	return null;
}
