/**
 * 记忆页「用量」卡（T8、R18）：中转计数，每种用途（另有嵌入、重排）最近 24 小时 / 7 天的调用次数与输入、输出 token。
 * 窄屏放不下两段并排的六列，所以用标签切换时间窗。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryPurpose,
	useMemoryUsage,
} from "../../../../api/memory-queries";
import { CountPill } from "../../../../components/layout/pane-parts";
import { Card } from "../../../../components/ui/data";
import { Spinner, tabClass } from "../../../../components/ui/primitives";
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
		<Card
			title={t("memoryPage.usage.title")}
			icon="chart"
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
				<p className="text-faint text-sm">{t("memoryPage.usage.empty")}</p>
			) : (
				<table className="w-full text-sm">
					<thead>
						<tr className="text-left text-faint text-xs">
							<th className="py-1 pr-2 font-medium">
								{t("memoryPage.usage.purpose")}
							</th>
							<th className="py-1 pr-2 text-right font-medium">
								{t("memoryPage.usage.calls")}
							</th>
							<th className="py-1 pr-2 text-right font-medium">
								{t("memoryPage.usage.input")}
							</th>
							<th className="py-1 text-right font-medium">
								{t("memoryPage.usage.output")}
							</th>
						</tr>
					</thead>
					<tbody>
						{items.map((i) => {
							const c = i[win];
							return (
								<tr key={i.purpose} className="border-border border-t">
									<td className="py-1.5 pr-2">
										{isUsagePurpose(i.purpose)
											? t(`memoryPage.usagePurpose.${i.purpose}`)
											: i.purpose}
									</td>
									<td className="py-1.5 pr-2 text-right">
										<CountPill n={c.calls} />
									</td>
									<td className="py-1.5 pr-2 text-right tabular-nums">
										{formatTokens(c.inputTokens)}
									</td>
									<td className="py-1.5 text-right tabular-nums">
										{formatTokens(c.outputTokens)}
									</td>
								</tr>
							);
						})}
					</tbody>
				</table>
			)}
		</Card>
	);
}
