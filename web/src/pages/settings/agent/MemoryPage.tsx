/**
 * 设置 → Agent → 记忆（M16）：内置 Hindsight 记忆的管理页。同一项目的线程共享该项目的记忆，全局记忆所有项目都能召回，
 * 项目之间互相看不到；隔离由控制面的记忆网关强制。作用于 orb 与 runner，不作用于全局助手。
 * 卡片顺序：状态 → 连接与 .env → 项目开关 → 模型 → 嵌入与重排 → 浏览 → 全局记忆 → 待批准 → mental models → 用量 → 导出。
 * 之后是「助手记忆」分区（M17）：全局助手关于主人的记忆（bank `oryn-owner`），依赖上面的工程记忆已启用并配置；
 * 未满足时分区只显示一条提示。分区内：设置 → 状态 → 主题 → 事实 → 来源与排除 → 明确记忆。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useOwnerMemorySettings } from "../../../api/owner-memory-queries";
import { ApplyStatusBanner } from "../../../components/settings/ApplyStatusBanner";
import { Callout } from "../../../components/ui/data";
import { Spinner } from "../../../components/ui/primitives";
import { BrowseCard } from "./memory/BrowseCard";
import { CandidatesCard } from "./memory/CandidatesCard";
import { ConnectionCard } from "./memory/ConnectionCard";
import { EndpointsCard } from "./memory/EndpointsCard";
import { ExportCard } from "./memory/ExportCard";
import { MemoryError } from "./memory/errors";
import { GlobalCard } from "./memory/GlobalCard";
import { MentalModelsCard } from "./memory/MentalModelsCard";
import { ModelsCard } from "./memory/ModelsCard";
import { OwnerExplicitCard } from "./memory/OwnerExplicitCard";
import { OwnerFactsCard } from "./memory/OwnerFactsCard";
import { OwnerSettingsCard } from "./memory/OwnerSettingsCard";
import { OwnerSourcesCard } from "./memory/OwnerSourcesCard";
import { OwnerStatusCard } from "./memory/OwnerStatusCard";
import { OwnerTopicsCard } from "./memory/OwnerTopicsCard";
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
			<OwnerMemorySection />
		</>
	);
}

/** 「助手记忆」分区：工程记忆未启用或未配置（`available` 为假）时只显示提示。 */
function OwnerMemorySection(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemorySettings();
	return (
		<section
			className="flex flex-col gap-3"
			aria-labelledby="owner-memory-heading"
			data-testid="owner-memory-section"
		>
			<h2
				id="owner-memory-heading"
				className="mt-4 font-semibold text-sm tracking-tight"
			>
				{t("memoryPage.owner.title")}
			</h2>
			<p className="text-faint text-xs">{t("memoryPage.owner.scope")}</p>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="ownerLoadSettings" error={q.error} />
			) : q.data && !q.data.available ? (
				<Callout tone="neutral" role="status">
					{t("memoryPage.owner.unavailable")}
				</Callout>
			) : q.data ? (
				<>
					<OwnerSettingsCard settings={q.data} />
					<OwnerStatusCard />
					<OwnerTopicsCard />
					<OwnerFactsCard enabled={q.data.enabled} />
					<OwnerSourcesCard />
					<OwnerExplicitCard />
				</>
			) : null}
		</section>
	);
}
