/**
 * artifact 卡片（计划 M14 2.4）：两种形态都是单个可聚焦的按钮。
 * - `tile`：上半部 16:10 的预览区（这一步是 `bg-inset` 上居中的大号类型图标；4.2 换成缩略图），下面是标题与
 *   「类型 · v版本 · 相对时间」。
 * - `row`：一行：类型图标、标题、`v版本`（转录卡片、首屏最近项）。
 * 不传 `onOpen` 时（只读的分享页）渲染成不可点的静态块。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Artifact } from "../../api/artifact-queries";
import { kindIcon, kindLabelKey } from "../../lib/artifacts/kinds";
import { formatRelative } from "../../lib/format";
import { Icon } from "../ui/icons";

/** 卡片只用到这几项；转录卡片从工具结果的 `details` 构造，不发请求。 */
export type ArtifactCardData = Pick<
	Artifact,
	"id" | "title" | "kind" | "version"
> &
	Partial<Pick<Artifact, "updatedAt">>;

export function ArtifactCard({
	artifact,
	form,
	onOpen,
}: {
	artifact: ArtifactCardData;
	form: "tile" | "row";
	onOpen?(id: string): void;
}): ReactNode {
	const { t } = useTranslation();
	const kind = t(kindLabelKey(artifact.kind));
	const label = t("artifacts.card.open", { title: artifact.title });
	const interactive = onOpen !== undefined;
	const base = `group w-full min-w-0 text-left transition-[background-color,border-color,box-shadow] duration-150 ${interactive ? "cursor-pointer hover:border-border-strong hover:bg-hover" : ""}`;
	const body =
		form === "row" ? (
			<>
				<Icon
					name={kindIcon(artifact.kind)}
					size={15}
					className="shrink-0 text-faint"
				/>
				<span className="min-w-0 flex-1 truncate font-medium text-sm">
					{artifact.title}
				</span>
				<span className="shrink-0 font-mono text-2xs text-muted tabular-nums">
					{t("artifacts.viewer.version", { n: artifact.version })}
				</span>
			</>
		) : (
			<>
				<span className="flex aspect-[16/10] w-full items-center justify-center bg-inset">
					<Icon
						name={kindIcon(artifact.kind)}
						size={36}
						className="text-faint"
					/>
				</span>
				<span className="flex min-w-0 flex-col gap-0.5 px-3 py-2.5">
					<span className="truncate font-medium text-sm">{artifact.title}</span>
					<span className="truncate text-muted text-xs">
						{artifact.updatedAt !== undefined
							? t("artifacts.card.meta", {
									kind,
									version: artifact.version,
									when: formatRelative(artifact.updatedAt),
								})
							: `${kind} · ${t("artifacts.viewer.version", { n: artifact.version })}`}
					</span>
				</span>
			</>
		);
	const shape =
		form === "row"
			? "flex items-center gap-2 rounded-md border border-border bg-panel px-2.5 py-1.5"
			: "flex flex-col overflow-hidden rounded-lg border border-border bg-panel";
	if (!interactive)
		return (
			<div
				data-testid="artifact-card"
				data-form={form}
				className={`${shape} ${base}`}
			>
				{body}
			</div>
		);
	return (
		<button
			type="button"
			data-testid="artifact-card"
			data-form={form}
			aria-label={label}
			title={label}
			className={`${shape} ${base}`}
			onClick={() => onOpen(artifact.id)}
		>
			{body}
		</button>
	);
}
