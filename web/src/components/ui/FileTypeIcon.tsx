/**
 * 内置文件类型图标组件（Catppuccin Icons Latte/Mocha 双色 + Material Icon Theme 兜底）。
 * 纯内联静态 SVG 渲染：无外链、无 dangerouslySetInnerHTML、无 <defs> id 碰撞，
 * Catppuccin 图标通过 CSS 变量自动跟随 Oryn 明暗主题。
 */
import { createElement, memo, type ReactNode, type SVGProps } from "react";
import { type FileEntryKind, resolveEntryIconKey } from "../../lib/file-icon";
import {
	CTP_VAR,
	FILE_TYPE_ICONS,
	type FileTypeIconKey,
	ICON_VIEWBOX,
	type SvgNode,
} from "./file-type-icons.gen";

/** SVG 短横线属性名 → React camelCase 属性名 */
const ATTR_MAP: Readonly<Record<string, string>> = {
	"stroke-width": "strokeWidth",
	"stroke-linecap": "strokeLinecap",
	"stroke-linejoin": "strokeLinejoin",
	"stroke-miterlimit": "strokeMiterlimit",
	"stroke-dasharray": "strokeDasharray",
	"fill-rule": "fillRule",
	"clip-rule": "clipRule",
	"fill-opacity": "fillOpacity",
	"stroke-opacity": "strokeOpacity",
};

function renderNodes(
	nodes: readonly SvgNode[],
	prefix = "",
): readonly ReactNode[] {
	return nodes.map((node, index) => {
		const [tag, rawAttrs, children] = node;
		const props: Record<string, unknown> = { key: `${prefix}${index}` };
		for (const [k, v] of Object.entries(rawAttrs)) {
			const rk = Object.hasOwn(ATTR_MAP, k) ? (ATTR_MAP[k] as string) : k;
			props[rk] =
				(rk === "fill" || rk === "stroke") && Object.hasOwn(CTP_VAR, v)
					? CTP_VAR[v]
					: v;
		}
		if (children && children.length > 0) {
			return createElement(
				tag,
				props,
				renderNodes(children, `${prefix}${index}-`),
			);
		}
		return createElement(tag, props);
	});
}

export interface FileTypeIconProps
	extends Omit<SVGProps<SVGSVGElement>, "name" | "children"> {
	/** 文件名、目录名或路径（含末尾 `/` 时若未指定 kind 会按目录处理） */
	name: string;
	/** 条目种类，默认按是否以 `/` 结尾判断 dir 或 file */
	kind?: FileEntryKind;
	/** 目录是否展开 */
	open?: boolean;
	/** 可选 MIME 类型（无扩展名时兜底） */
	mime?: string;
	/** 图标像素尺寸，默认 16 */
	size?: number;
	/** 直接指定图标键（跳过路径解析） */
	iconKey?: FileTypeIconKey;
}

export const FileTypeIcon = memo(function FileTypeIcon({
	name,
	kind,
	open = false,
	mime,
	size = 16,
	iconKey,
	className,
	...rest
}: FileTypeIconProps): ReactNode {
	const resolvedKind: FileEntryKind =
		kind ?? (name.endsWith("/") ? "dir" : "file");
	const key =
		iconKey && Object.hasOwn(FILE_TYPE_ICONS, iconKey)
			? iconKey
			: resolveEntryIconKey(name, resolvedKind, open, mime);
	const nodes = FILE_TYPE_ICONS[key] ?? FILE_TYPE_ICONS._file;
	const viewBox = Object.hasOwn(ICON_VIEWBOX, key)
		? (ICON_VIEWBOX[key] as string)
		: "0 0 16 16";

	return (
		// biome-ignore lint/a11y/noSvgWithoutTitle: 装饰性图标默认 aria-hidden，有 aria-label 时由无障碍名称承担
		<svg
			xmlns="http://www.w3.org/2000/svg"
			viewBox={viewBox}
			width={size}
			height={size}
			fill="none"
			data-file-icon={key}
			aria-hidden={rest["aria-label"] ? undefined : true}
			focusable="false"
			className={className ? `shrink-0 ${className}` : "shrink-0"}
			{...rest}
		>
			{nodes ? renderNodes(nodes) : null}
		</svg>
	);
});
