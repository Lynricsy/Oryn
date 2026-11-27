/**
 * 设置分区注册表：路由、设置页内导航、手机分组首页、命令面板共用这一份。顺序即显示顺序。
 * 分组按「这项设置影响谁」：个人（我和这台设备）/ 助手与 Agent / 工作区资源 / 其它。见 docs/design/settings-ia.md。
 */
import type { IconName } from "../../components/ui/icons";

export type SettingsGroup = "personal" | "agent" | "workspace" | "other";

export const SETTINGS_GROUPS: readonly SettingsGroup[] = [
	"personal",
	"agent",
	"workspace",
	"other",
];

export const SETTINGS_SECTIONS = [
	"account",
	"appearance",
	"input",
	"notifications",
	"desktop",
	"assistant",
	"agent-models",
	"agent-subagents",
	"agent-instructions",
	"agent-extensions",
	"agent-memory",
	"agent-config",
	"automations",
	"compute",
	"server",
	"secrets",
	"ssh",
	"git",
	"browser-profiles",
	"sharing",
	"privacy",
	"about",
] as const;

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number];

export interface SettingsEntry {
	id: SettingsSection;
	group: SettingsGroup;
	icon: IconName;
	/** 只在桌面 App 里出现 */
	desktopOnly?: boolean;
}

const GROUP_OF: Record<SettingsSection, SettingsGroup> = {
	account: "personal",
	appearance: "personal",
	input: "personal",
	notifications: "personal",
	desktop: "personal",
	assistant: "agent",
	"agent-models": "agent",
	"agent-subagents": "agent",
	"agent-instructions": "agent",
	"agent-extensions": "agent",
	"agent-memory": "agent",
	"agent-config": "agent",
	automations: "workspace",
	compute: "workspace",
	server: "workspace",
	secrets: "workspace",
	ssh: "workspace",
	git: "workspace",
	"browser-profiles": "workspace",
	sharing: "other",
	privacy: "other",
	about: "other",
};

const ICON_OF: Record<SettingsSection, IconName> = {
	account: "user",
	appearance: "palette",
	input: "keyboard",
	notifications: "bell",
	desktop: "monitor",
	assistant: "globe",
	"agent-models": "repeat",
	"agent-subagents": "tree",
	"agent-instructions": "fileText",
	"agent-extensions": "plugs",
	"agent-memory": "brain",
	"agent-config": "file",
	automations: "clock",
	compute: "box",
	server: "sliders",
	secrets: "key",
	ssh: "terminal",
	git: "github",
	"browser-profiles": "browser",
	sharing: "share",
	privacy: "shield",
	about: "info",
};

export const SETTINGS_ENTRIES: readonly SettingsEntry[] = SETTINGS_SECTIONS.map(
	(id) => ({
		id,
		group: GROUP_OF[id],
		icon: ICON_OF[id],
		...(id === "desktop" ? { desktopOnly: true } : {}),
	}),
);

/** 当前宿主下可见的分区（浏览器与移动壳里没有「桌面应用」）。 */
export function visibleEntries(desktop: boolean): readonly SettingsEntry[] {
	return SETTINGS_ENTRIES.filter((e) => !e.desktopOnly || desktop);
}

/** 重排前的分区 id → 新位置；旧链接（推送、文档、书签）经路由重定向过来。 */
export const LEGACY_SECTIONS: Readonly<Record<string, SettingsSection>> = {
	devices: "account",
	identity: "account",
	"agent-composer": "input",
	"selection-actions": "input",
	"agent-roles": "agent-models",
	"agent-skills": "agent-instructions",
	"agent-prompts": "agent-instructions",
	"agent-commands": "agent-instructions",
	"agent-ssh": "ssh",
	providers: "compute",
	runners: "compute",
	"git-identity": "git",
	github: "git",
	shares: "sharing",
	artifacts: "sharing",
};
