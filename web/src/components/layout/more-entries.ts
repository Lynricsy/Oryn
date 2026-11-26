/**
 * 「更多」分区的条目注册表（M9.6）：「更多」侧栏的条目只在这里注册，侧栏与测试都从这里取。
 * 顺序即显示顺序；第一项是「更多」分区的缺省落点（`sections.ts` 的 `defaultSectionPath("more")`）。
 * 设置只占一行：各分区的导航在设置页里（`pages/settings/registry.ts`）。
 */
import type { IconName } from "../ui/icons";

export interface MoreEntry {
	key: string;
	group: "settings" | "overview";
	icon: IconName;
	/** i18n 键 */
	labelKey: string;
	/** 站内路径；pathname 等于它或以 `它 + "/"` 开头时高亮 */
	to: string;
}

export const MORE_ENTRIES: readonly MoreEntry[] = [
	{
		key: "settings",
		group: "settings",
		icon: "settings",
		labelKey: "nav.settings",
		to: "/settings",
	},
	{
		// 统计分析（M10）：独立页 /stats（用量 / Orb / 产出）
		key: "stats",
		group: "overview",
		icon: "chart",
		labelKey: "nav.stats",
		to: "/stats",
	},
	{
		// Artifacts（M14）：agent 产出的交付物总览
		key: "artifacts",
		group: "overview",
		icon: "shapes",
		labelKey: "nav.artifacts",
		to: "/artifacts",
	},
	{
		// Boxes（M23）：常驻机器与应用部署
		key: "boxes",
		group: "overview",
		icon: "hardDrives",
		labelKey: "nav.boxes",
		to: "/boxes",
	},
	{
		// Provider 算力与资源总览：节点健康、Orb 计数、内存/存储/CPU 监控面板
		key: "providers-overview",
		group: "overview",
		icon: "gauge",
		labelKey: "nav.providersOverview",
		to: "/providers",
	},
];
