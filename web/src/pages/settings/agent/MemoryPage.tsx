/**
 * 设置 → Agent → 记忆（M16）：内置 Hindsight 记忆的管理页。同一项目的线程共享该项目的记忆，全局记忆所有项目都能召回，
 * 项目之间互相看不到；隔离由控制面的记忆网关强制。作用于 orb 与 runner，不作用于全局助手。
 * 卡片顺序：状态 → 连接与 .env → 项目开关 → 模型 → 嵌入与重排 → 浏览 → 全局记忆 → 待批准 → mental models → 用量 → 导出。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApplyStatusBanner } from "../../../components/settings/ApplyStatusBanner";
import { Callout } from "../../../components/ui/data";
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
			<ApplyStatusBanner />
			<Callout tone="neutral">{t("memoryPage.scope")}</Callout>
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
