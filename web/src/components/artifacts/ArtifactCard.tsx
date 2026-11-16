/**
 * artifact 卡片（计划 M14 2.4、4.2）：两种形态都是单个可聚焦的按钮。
 * - `tile`：上半部 16:10 的预览区——缩略图（`<img loading="lazy">`，服务端每个版本渲染一张），加载失败（还没有、
 *   渲染不可用）回落到 `bg-inset` 上居中的大号类型图标；下面是标题与「类型 · v版本 · 相对时间」（`meta` 可换掉这一行）。
 * - `row`：一行：类型图标、标题、`v版本`（转录卡片、首屏最近项）。
 * 不传 `onOpen` 时（只读的分享页）渲染成不可点的静态块。
 */
import {
	type KeyboardEvent,
	type MouseEvent,
	type ReactNode,
	type Ref,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type Artifact,
	artifactThumbnailUrl,
} from "../../api/artifact-queries";
import { kindIcon, kindLabelKey } from "../../lib/artifacts/kinds";
import { formatRelative } from "../../lib/format";
import { Icon } from "../ui/icons";

/** 卡片只用到这几项；转录卡片从工具结果的 `details` 构造，不发请求。 */
export type ArtifactCardData = Pick<
	Artifact,
	"id" | "title" | "kind" | "version"
> &
	Partial<Pick<Artifact, "updatedAt">>;

/** tile 的预览区：缩略图，加载失败回落到类型示意图。版本变了重新尝试。 */
function Preview({ artifact }: { artifact: ArtifactCardData }): ReactNode {
	const src = artifactThumbnailUrl(artifact.id, artifact.version);
	const [failed, setFailed] = useState<string | null>(null);
	return (
		<span className="relative flex aspect-[16/10] w-full items-center justify-center overflow-hidden bg-inset">
			<Icon name={kindIcon(artifact.kind)} size={36} className="text-faint" />
			{failed !== src && (
				<img
					src={src}
					alt=""
					loading="lazy"
					decoding="async"
					draggable={false}
					data-testid="artifact-thumb"
					className="absolute inset-0 h-full w-full object-cover object-top"
					onError={() => setFailed(src)}
				/>
			)}
		</span>
	);
}

export function ArtifactCard({
	artifact,
	form,
	onOpen,
	meta,
	ref,
	tabIndex,
	onKeyDown,
	onContextMenu,
	onFocus,
}: {
	artifact: ArtifactCardData;
	form: "tile" | "row";
	onOpen?(id: string): void;
	/** 换掉 tile 的说明行（回收站里的「剩 N 天」）。 */
	meta?: ReactNode;
	ref?: Ref<HTMLButtonElement>;
	/** 总览页的卡片网格用漫游 tabIndex。 */
	tabIndex?: number;
	onKeyDown?(e: KeyboardEvent<HTMLButtonElement>): void;
	onContextMenu?(e: MouseEvent<HTMLButtonElement>): void;
	onFocus?(): void;
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
				<Preview artifact={artifact} />
				<span className="flex min-w-0 flex-col gap-0.5 px-3 py-2.5">
					<span className="truncate font-medium text-sm">{artifact.title}</span>
					<span className="truncate text-muted text-xs">
						{meta ??
							(artifact.updatedAt !== undefined
								? t("artifacts.card.meta", {
										kind,
										version: artifact.version,
										when: formatRelative(artifact.updatedAt),
									})
								: `${kind} · ${t("artifacts.viewer.version", { n: artifact.version })}`)}
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
			ref={ref}
			type="button"
			data-testid="artifact-card"
			data-form={form}
			data-artifact-id={artifact.id}
			aria-label={label}
			title={label}
			tabIndex={tabIndex}
			className={`${shape} ${base}`}
			onClick={() => onOpen(artifact.id)}
			onKeyDown={onKeyDown}
			onContextMenu={onContextMenu}
			onFocus={onFocus}
		>
			{body}
		</button>
	);
}
