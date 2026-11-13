import type { ReactNode } from "react";
import { type Brand, modelBrand } from "../../lib/model-brand";
import { Icon } from "./icons";

// 每个品牌一个 svg（取自 @lobehub/icons-static-svg，见同目录 LICENSE）。
// 不内联、也不打进 JS：`no-inline` 让每个 logo 成为单独的静态资源，用到才下载。
const URLS = import.meta.glob<string>("../../assets/model-logos/*.svg", {
	eager: true,
	query: "?url&no-inline",
	import: "default",
});

function logoUrl(brand: Brand): string | undefined {
	return URLS[`../../assets/model-logos/${brand}.svg`];
}

/**
 * 单色 logo：用 mask 叠 currentColor，跟随文字色，明暗主题都清楚。
 * 其余是彩色 logo，原样用 <img> 显示（彩色 svg 里的 <defs> id 互相隔离，不会撞）。
 * qwen、exa 的彩色版是深蓝，深色主题下看不清，所以也用单色。
 */
const MONO: Partial<Record<Brand, true>> = {
	openai: true,
	xai: true,
	qwen: true,
	moonshot: true,
	cohere: true,
	zeroone: true,
	ibm: true,
	internlm: true,
	ai21: true,
	xiaomi: true,
	longcat: true,
	liquid: true,
	exa: true,
};

/**
 * 模型厂商 logo。`model` 可以是 id、显示名或 `provider/id`；认不出时回退为 sparkle 图标。
 * 纯装饰（aria-hidden），无障碍名由调用方旁边的文字承担。
 */
export function ModelLogo({
	model,
	provider,
	size = 14,
	className = "",
}: {
	model?: string;
	provider?: string;
	size?: number;
	className?: string;
}): ReactNode {
	const brand = modelBrand(model, provider);
	const url = brand ? logoUrl(brand) : undefined;
	if (!brand || !url)
		return (
			<Icon
				name="sparkle"
				size={size}
				aria-hidden
				className={`shrink-0 ${className}`}
			/>
		);
	if (MONO[brand]) {
		const mask = `url("${url}") center / contain no-repeat`;
		return (
			<span
				aria-hidden
				data-brand={brand}
				className={`inline-block shrink-0 bg-current ${className}`}
				style={{ width: size, height: size, mask, WebkitMask: mask }}
			/>
		);
	}
	return (
		<img
			src={url}
			alt=""
			aria-hidden
			data-brand={brand}
			width={size}
			height={size}
			draggable={false}
			className={`inline-block shrink-0 select-none ${className}`}
		/>
	);
}
