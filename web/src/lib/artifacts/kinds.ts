/**
 * artifact 的四种类型（`kind`）：图标与界面名的 i18n key。
 * 纯数据，沙箱里的查看器也可以 import（只有类型依赖图标表）。
 */
import type { IconName } from "../../components/ui/icons";

export type ArtifactKind = "web" | "canvas" | "document" | "slides";

export const ARTIFACT_KINDS: readonly ArtifactKind[] = [
	"document",
	"slides",
	"web",
	"canvas",
];

export const KIND_ICON: Record<ArtifactKind, IconName> = {
	web: "browsers",
	canvas: "shapes",
	document: "fileText",
	slides: "presentation",
};

export function isArtifactKind(x: unknown): x is ArtifactKind {
	return (
		typeof x === "string" && (ARTIFACT_KINDS as readonly string[]).includes(x)
	);
}

/** 类型名的 i18n key（`artifacts.kinds.*`）；不认识的类型按页面处理。 */
export function kindLabelKey(kind: string): `artifacts.kinds.${ArtifactKind}` {
	return `artifacts.kinds.${isArtifactKind(kind) ? kind : "web"}`;
}

/** 类型图标；不认识的类型按页面处理。 */
export function kindIcon(kind: string): IconName {
	return KIND_ICON[isArtifactKind(kind) ? kind : "web"];
}
