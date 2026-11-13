/** 头像方块：agent 头像与用户头像共用的渲染——设置里上传的图片、选的单个字形，或各自的默认内容。 */
import type { CSSProperties, ReactNode } from "react";
import type { Schemas } from "../../api/client";
import type { AvatarOwner } from "../../api/queries";

/** 方块底色：agent 用强调色浅底；用户用中性色，避免和「需要你」的强调色混淆。 */
const TONE = {
	accent: "bg-accent-soft text-accent-ink",
	neutral: "bg-raised text-muted ring-1 ring-border ring-inset",
} as const;

export function AvatarTile({
	of,
	identity,
	size,
	tone,
	fallback,
}: {
	/** 写到 `data-avatar-of`，便于测试与截图脚本区分两种头像。 */
	of: AvatarOwner;
	identity: Schemas["AvatarIdentity"] | undefined;
	size: number;
	tone: keyof typeof TONE;
	/** 未设置时方块里的内容（品牌标、名字首字或图标）。 */
	fallback: ReactNode;
}): ReactNode {
	const box: CSSProperties = { width: size, height: size };
	if (identity?.avatarImage)
		return (
			<img
				src={identity.avatarImage}
				alt=""
				aria-hidden
				data-avatar="image"
				data-avatar-of={of}
				width={size}
				height={size}
				className="shrink-0 rounded-md object-cover ring-1 ring-border"
				style={box}
			/>
		);
	const icon = identity?.avatarIcon;
	return (
		<span
			aria-hidden
			data-avatar={icon ? "icon" : "default"}
			data-avatar-of={of}
			className={`inline-grid shrink-0 select-none place-items-center overflow-hidden rounded-md leading-none ${TONE[tone]}`}
			style={icon ? { ...box, fontSize: Math.round(size * 0.66) } : box}
		>
			{icon ?? fallback}
		</span>
	);
}
