/**
 * 「助手记忆」（M17）：全局助手关于主人的记忆（bank `oryn-owner`），放在设置 → 全局助手的末尾。
 * 依赖 Agent 记忆（Hindsight，设置 → 记忆）已启用并配置；未满足（`available` 为假）时只有「助手记忆」一组、一行提示。
 * 分组：助手记忆（开关、名字与别名）→ 判断模型 → 助手记忆状态 → 主题 → 事实 → 来源 → 排除项 → 明确记忆。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useOwnerMemorySettings } from "../../../../api/owner-memory-queries";
import { GroupNote } from "../../../../components/settings/agent-config-ui";
import { Spinner } from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
} from "../../../../components/ui/workbench";
import { MemoryError } from "./errors";
import { OwnerExplicitCard } from "./OwnerExplicitCard";
import { OwnerFactsCard } from "./OwnerFactsCard";
import { OwnerDecideCard, OwnerSettingsCard } from "./OwnerSettingsCard";
import { OwnerExclusionsCard, OwnerSourcesCard } from "./OwnerSourcesCard";
import { OwnerStatusCard } from "./OwnerStatusCard";
import { OwnerTopicsCard } from "./OwnerTopicsCard";

export function OwnerMemorySection(): ReactNode {
	const { t } = useTranslation();
	const q = useOwnerMemorySettings();
	const data = q.data?.available ? q.data : undefined;
	return (
		// 各组仍是设置页内容区的直接子项（display: contents），沿用外壳的分组间距
		<div data-testid="owner-memory-section" className="contents">
			<Group title={t("memoryPage.owner.title")}>
				{q.isPending ? (
					<Spinner />
				) : q.error ? (
					<MemoryError step="ownerLoadSettings" error={q.error} />
				) : data ? (
					<OwnerSettingsCard settings={data} />
				) : (
					<GroupList>
						<GroupRow
							icon="memory"
							title={t("memoryPage.owner.unavailableTitle")}
							meta={t("memoryPage.owner.unavailable")}
						/>
					</GroupList>
				)}
				<GroupNote>{t("memoryPage.owner.scope")}</GroupNote>
			</Group>
			{data && (
				<>
					<OwnerDecideCard decide={data.decide} />
					<OwnerStatusCard />
					<OwnerTopicsCard />
					<OwnerFactsCard enabled={data.enabled} />
					<OwnerSourcesCard />
					<OwnerExclusionsCard />
					<OwnerExplicitCard />
				</>
			)}
		</div>
	);
}
