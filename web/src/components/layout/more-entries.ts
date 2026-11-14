/**
 * 「更多」分区的条目注册表（M9.6）：「更多」侧栏的条目只在这里注册，侧栏与测试都从这里取。
 * 顺序即显示顺序；第一项是「更多」分区的缺省落点（`sections.ts` 的 `defaultSectionPath("more")`）。
 */
import type { IconName } from "../ui/icons";

export interface MoreEntry {
	key: string;
	group: "overview" | "settings";
	icon: IconName;
	/** i18n 键 */
	labelKey: string;
	/** 站内路径；pathname 等于它或以 `它 + "/"` 开头时高亮 */
	to: string;
	/** 只在桌面 App 里出现 */
	desktopOnly?: boolean;
}

export const MORE_ENTRIES: readonly MoreEntry[] = [
	{
		key: "automations",
		group: "overview",
		icon: "clock",
		labelKey: "settings.automations",
		to: "/settings/automations",
	},
	{
		key: "notifications",
		group: "overview",
		icon: "bell",
		labelKey: "settings.notifications",
		to: "/settings/notifications",
	},
	{
		key: "identity",
		group: "settings",
		icon: "robot",
		labelKey: "settings.identity",
		to: "/settings/identity",
	},
	{
		key: "appearance",
		group: "settings",
		icon: "palette",
		labelKey: "settings.appearance",
		to: "/settings/appearance",
	},
	{
		key: "devices",
		group: "settings",
		icon: "device",
		labelKey: "settings.devices",
		to: "/settings/devices",
	},
	{
		key: "providers",
		group: "settings",
		icon: "box",
		labelKey: "settings.providers",
		to: "/settings/providers",
	},
	{
		key: "secrets",
		group: "settings",
		icon: "key",
		labelKey: "settings.secrets",
		to: "/settings/secrets",
	},
	{
		key: "browser-profiles",
		group: "settings",
		icon: "browser",
		labelKey: "settings.browser-profiles",
		to: "/settings/browser-profiles",
	},
	{
		key: "git-identity",
		group: "settings",
		icon: "fork",
		labelKey: "settings.git-identity",
		to: "/settings/git-identity",
	},
	{
		key: "agent-config",
		group: "settings",
		icon: "file",
		labelKey: "settings.agent-config",
		to: "/settings/agent-config",
	},
	{
		key: "shares",
		group: "settings",
		icon: "share",
		labelKey: "settings.shares",
		to: "/settings/shares",
	},
	{
		key: "desktop",
		group: "settings",
		icon: "monitor",
		labelKey: "settings.desktop",
		to: "/settings/desktop",
		desktopOnly: true,
	},
	{
		key: "about",
		group: "settings",
		icon: "info",
		labelKey: "settings.about",
		to: "/settings/about",
	},
];
