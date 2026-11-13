/**
 * 认证类页面的居中卡片：品牌标 + 字标 + 标题 + 内容。`/login`、`/setup`、`/pair` 与桌面壳的连接页共用，外观一致。
 * `blink`：品牌标的插入符闪烁，表示在等你操作（登录、配对页用）。`footer`：卡片底部的小字（连接页的版本与存储提示）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { BrandMark } from "./ui/data";

export function AuthCard({
	title,
	blink = false,
	children,
	footer,
}: {
	title: string;
	blink?: boolean;
	children: ReactNode;
	footer?: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="shell-wash flex min-h-dvh items-center justify-center px-4 pt-safe pb-safe">
			<div className="glass pop-in w-full max-w-sm rounded-2xl p-6">
				<div className="mb-4 flex items-center gap-2.5">
					<BrandMark size={32} blink={blink} />
					<span className="wordmark text-[28px] text-fg leading-none">
						{t("app.name")}
					</span>
				</div>
				<h1 className="mb-4 font-semibold text-lg tracking-tight">{title}</h1>
				{children}
				{footer && (
					<div className="mt-5 flex flex-col gap-1 border-border border-t pt-3 text-2xs text-faint">
						{footer}
					</div>
				)}
			</div>
		</div>
	);
}
