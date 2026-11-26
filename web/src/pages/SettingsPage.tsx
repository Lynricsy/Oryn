/**
 * 设置页：页头 + 设置自带的分组导航（注册表见 `./settings/registry.ts`，导航见 `./settings/SettingsNav.tsx`）。
 * - 宽屏（medium / expanded）：左侧导航栏（搜索 + 四组），右侧是当前分区；`/settings` 落到第一个分区。
 * - 手机（compact）：`/settings` 是分组首页，点进分区后页头左侧是返回。
 * 「桌面应用」分区（M6）只在桌面壳内出现，浏览器与移动壳访问时回到设置首页。项目设置在项目分区（`/projects/<id>`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useNavigate } from "react-router";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { Sheet } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import { IconButton } from "../components/ui/primitives";
import { host } from "../lib/host";
import { useBreakpoint } from "../lib/viewport";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";
import AboutSettings from "./settings/AboutSettings";
import AccountSettings from "./settings/AccountSettings";
import AgentConfigSettings from "./settings/AgentConfigSettings";
import AppearanceSettings from "./settings/AppearanceSettings";
import AssistantSettings from "./settings/AssistantSettings";
import AutomationsSettings from "./settings/AutomationsSettings";
import ExtensionsPage from "./settings/agent/ExtensionsPage";
import InstructionsPage from "./settings/agent/InstructionsPage";
import MemoryPage from "./settings/agent/MemoryPage";
import RolesPage from "./settings/agent/RolesPage";
import SshPage from "./settings/agent/SshPage";
import SubagentsPage from "./settings/agent/SubagentsPage";
import BrowserProfilesSettings from "./settings/BrowserProfilesSettings";
import ComputeSettings from "./settings/ComputeSettings";
import DesktopSettings from "./settings/DesktopSettings";
import GitSettings from "./settings/GitSettings";
import InputSettings from "./settings/InputSettings";
import NotificationsSettings from "./settings/NotificationsSettings";
import { SETTINGS_SECTIONS, type SettingsSection } from "./settings/registry";
import SecretsSettings from "./settings/SecretsSettings";
import { SettingsNav } from "./settings/SettingsNav";
import SharingSettings from "./settings/SharingSettings";

/** 各分区的内容 */
const VIEWS: Record<SettingsSection, () => ReactNode> = {
	account: () => <AccountSettings />,
	appearance: () => <AppearanceSettings />,
	input: () => <InputSettings />,
	notifications: () => <NotificationsSettings />,
	desktop: () => <DesktopSettings />,
	assistant: () => <AssistantSettings />,
	"agent-models": () => <RolesPage />,
	"agent-subagents": () => <SubagentsPage />,
	"agent-instructions": () => <InstructionsPage />,
	"agent-extensions": () => <ExtensionsPage />,
	"agent-memory": () => <MemoryPage />,
	"agent-config": () => <AgentConfigSettings />,
	automations: () => <AutomationsSettings />,
	compute: () => <ComputeSettings />,
	secrets: () => <SecretsSettings />,
	ssh: () => <SshPage />,
	git: () => <GitSettings />,
	"browser-profiles": () => <BrowserProfilesSettings />,
	sharing: () => <SharingSettings />,
	about: () => <AboutSettings />,
};

/** `section` 缺省即设置首页（`/settings`）。 */
export function SettingsPage({
	section,
}: {
	section?: SettingsSection;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const compact = bp === "compact";
	const sidebarToggle = useSidebarToggleShown();
	// 桌面 App 无系统标题栏：页头可拖动，两端按需放窗口控件
	const chromeLeft = !useSidebarShown();
	// 「桌面应用」分区只在桌面壳里有：浏览器与移动壳都没有
	if (section === "desktop" && host.desktop === null)
		return <Navigate to="/settings" replace />;
	// 宽屏没有单独的首页：落到第一个分区
	if (!section && !compact)
		return <Navigate to={`/settings/${SETTINGS_SECTIONS[0]}`} replace />;
	const View = section ? VIEWS[section] : null;
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header
				data-window-drag=""
				className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
			>
				<ChromeLead show={chromeLeft} />
				{compact && section ? (
					<IconButton
						icon="back"
						label={t("settings.back")}
						onClick={() => navigate("/settings")}
					/>
				) : (
					sidebarToggle && (
						<IconButton
							icon={bp === "expanded" ? "sidebar" : "menu"}
							label={t("nav.sidebar")}
							onClick={() => uiStore.getState().toggleSidebar()}
						/>
					)
				)}
				{!(compact && section) && (
					<>
						<Icon name="settings" size={16} className="text-faint" />
						<h1 className="font-semibold text-sm">{t("nav.settings")}</h1>
					</>
				)}
				{section && (
					<>
						{!compact && (
							<span aria-hidden className="text-ghost text-sm">
								/
							</span>
						)}
						<span
							className={`truncate text-sm ${compact ? "font-semibold" : "text-muted"}`}
						>
							{t(`settings.${section}`)}
						</span>
					</>
				)}
				<span className="min-w-0 flex-1" />
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div className="flex min-h-0 flex-1">
				{!compact && (
					<div className="w-56 shrink-0 overflow-y-auto border-border border-r p-3 pb-safe">
						<SettingsNav variant="rail" />
					</div>
				)}
				<div className="min-h-0 flex-1 overflow-y-auto p-3 pb-safe sm:p-5">
					<div className="mx-auto flex max-w-3xl flex-col gap-[30px]">
						{View ? <View /> : <SettingsNav variant="index" />}
					</div>
				</div>
			</div>
		</Sheet>
	);
}
