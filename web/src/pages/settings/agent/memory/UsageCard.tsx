/**
 * 「用量」分组（T8、R18）：中转计数，每种用途（另有嵌入、重排）最近 24 小时 / 7 天的调用次数与输入、输出 token，
 * 一个用途一行；分组标题右侧切换时间窗。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryPurpose,
	useMemoryUsage,
} from "../../../../api/memory-queries";
import { EmptyPanel } from "../../../../components/settings/agent-config-ui";
import { Spinner, tabClass } from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
} from "../../../../components/ui/workbench";
import { formatTokens } from "../../../../lib/format";
import { MemoryError } from "./errors";

type Window = "day" | "week";

/** 计数里出现的用途：五种模型用途与嵌入、重排。 */
type UsagePurpose = MemoryPurpose | "embed" | "rerank";
const USAGE_PURPOSES: Record<UsagePurpose, true> = {
	default: true,
	retain: true,
	reflect: true,
	consolidation: true,
	mental_model: true,
	embed: true,
	rerank: true,
};

/** 认识的用途给显示名，未知的（控制面以后新增）原样显示。 */
function isUsagePurpose(p: string): p is UsagePurpose {
	return Object.hasOwn(USAGE_PURPOSES, p);
}

export function UsageCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryUsage();
	const [win, setWin] = useState<Window>("day");
	const items = q.data?.items ?? [];
	return (
		<Group
			title={t("memoryPage.usage.title")}
			action={
				<div role="tablist" className="flex gap-0.5">
					{(["day", "week"] as const).map((w) => (
						<button
							key={w}
							type="button"
							role="tab"
							aria-selected={win === w}
							className={`${tabClass(win === w)} ${win === w ? "bg-hover" : ""}`}
							onClick={() => setWin(w)}
						>
							{t(`memoryPage.usage.${w}`)}
						</button>
					))}
				</div>
			}
		>
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadUsage" error={q.error} />
			) : items.length === 0 ? (
				<EmptyPanel>{t("memoryPage.usage.empty")}</EmptyPanel>
			) : (
				<GroupList>
					{items.map((i) => {
						const c = i[win];
						return (
							<GroupRow
								key={i.purpose}
								title={
									isUsagePurpose(i.purpose)
										? t(`memoryPage.usagePurpose.${i.purpose}`)
										: i.purpose
								}
								trailing={
									<span className="flex flex-wrap justify-end gap-x-3 text-[12.5px] text-muted tabular-nums">
										<span>
											{t("memoryPage.usage.calls")} {c.calls}
										</span>
										<span>
											{t("memoryPage.usage.input")}{" "}
											{formatTokens(c.inputTokens)}
										</span>
										<span>
											{t("memoryPage.usage.output")}{" "}
											{formatTokens(c.outputTokens)}
										</span>
									</span>
								}
							/>
						);
					})}
				</GroupList>
			)}
		</Group>
	);
}
