/**
 * 设置 → Agent → 记忆（M16）：内置 Hindsight 记忆的管理页。同一项目的线程共享该项目的记忆，全局记忆所有项目都能召回，
 * 项目之间互相看不到；隔离由控制面的记忆网关强制。作用于 orb 与 runner，不作用于全局助手
 * （全局助手关于主人的记忆在「全局助手」分区）。
 * 分组顺序：状态 → 连接 → 项目 → 模型 → 嵌入与重排 → 浏览 → 全局记忆 → 待批准 → mental models → 用量 → 导出。
 * 不再有吸顶的页内目录：助手记忆搬走后只剩一个主题，各分组标题已足够定位，设置页自带分区导航，
 * 吸顶条叠在毛玻璃分组上也会多出一层导航。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApplyStatusBanner } from "../../../components/settings/ApplyStatusBanner";
import { GroupNote } from "../../../components/settings/agent-config-ui";
import { BrowseCard } from "./memory/BrowseCard";
import { CandidatesCard } from "./memory/CandidatesCard";
import { ConnectionCard } from "./memory/ConnectionCard";
import { EndpointsCard } from "./memory/EndpointsCard";
import { ExportCard } from "./memory/ExportCard";
import { GlobalCard } from "./memory/GlobalCard";
import { MentalModelsCard } from "./memory/MentalModelsCard";
import { ModelsCard } from "./memory/ModelsCard";
import { StatusCard } from "./memory/StatusCard";
import { ToggleCard } from "./memory/ToggleCard";
import { UsageCard } from "./memory/UsageCard";

export default function MemoryPage(): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{/* 作用范围（记忆页的说法比其它 Agent 子页更具体）+ 下发状态，只此一处 */}
			<div className="flex flex-col gap-2">
				<GroupNote>{t("memoryPage.scope")}</GroupNote>
				<ApplyStatusBanner />
			</div>
			<StatusCard />
			<ConnectionCard />
			<ToggleCard />
			<ModelsCard />
			<EndpointsCard />
			<BrowseCard />
			<GlobalCard />
			<CandidatesCard />
			<MentalModelsCard />
			<UsageCard />
			<ExportCard />
		</>
	);
}
