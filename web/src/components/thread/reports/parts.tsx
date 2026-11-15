/** 报告对话框共用的小件：加载 / 失败态、分区标题、卡片、读数格、计时器。与上下文报告同一套卡片外观。 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "../../../i18n";
import { Callout } from "../../ui/data";
import { Button, describeError, Spinner } from "../../ui/primitives";

/** 加载中：转圈 + 一句在读什么。 */
export function ReportLoading({ label }: { label: string }): ReactNode {
	return (
		<div className="flex items-center justify-center gap-2 py-12 text-muted text-sm">
			<Spinner />
			<span>{label}</span>
		</div>
	);
}

/** 读取失败：哪一步（标题）、为什么（错误原文）、下一步（提示 + 重试）。 */
export function ReportError({
	title,
	error,
	onRetry,
	retrying,
}: {
	title: string;
	error: unknown;
	onRetry: () => void;
	retrying?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Callout
			tone="err"
			role="alert"
			title={title}
			actions={
				<Button size="sm" icon="refresh" disabled={retrying} onClick={onRetry}>
					{t("report.retry")}
				</Button>
			}
		>
			<p className="break-words">{describeError(error, t)}</p>
			<p className="mt-1 text-muted">{t("report.failedHint")}</p>
		</Callout>
	);
}

/** 报告里的一个分区：小标题（可带右侧说明）+ 内容。 */
export function ReportSection({
	title,
	aside,
	children,
}: {
	title: ReactNode;
	aside?: ReactNode;
	children: ReactNode;
}): ReactNode {
	return (
		<section className="flex flex-col gap-1.5">
			<header className="flex min-h-5 items-center gap-2 px-0.5">
				<h3 className="min-w-0 truncate font-medium text-faint text-xs">
					{title}
				</h3>
				<span className="flex-1" />
				{aside}
			</header>
			{children}
		</section>
	);
}

/** 对话框正文里的卡片：12px 圆角与对话框外框同心。 */
export function ReportCard({
	children,
	className = "",
}: {
	children: ReactNode;
	className?: string;
}): ReactNode {
	return (
		<div
			className={`rounded-md border border-border bg-raised p-3 ${className}`}
		>
			{children}
		</div>
	);
}

export interface StatItem {
	key: string;
	label: ReactNode;
	value: ReactNode;
	/** 读数下方的淡色补充 */
	hint?: ReactNode;
}

/** 读数格：小标签在上、数值在下；窄屏两列，宽屏按 `cols` 列。 */
export function StatGrid({
	items,
	cols = 4,
}: {
	items: readonly StatItem[];
	cols?: 3 | 4;
}): ReactNode {
	return (
		<dl
			className={`grid grid-cols-2 gap-x-4 gap-y-3 ${cols === 4 ? "sm:grid-cols-4" : "sm:grid-cols-3"}`}
		>
			{items.map((it) => (
				<div key={it.key} className="flex min-w-0 flex-col gap-0.5">
					<dt className="truncate text-2xs text-faint">{it.label}</dt>
					<dd className="m-0 truncate text-sm tabular-nums">{it.value}</dd>
					{it.hint && (
						<dd className="m-0 truncate text-2xs text-faint">{it.hint}</dd>
					)}
				</div>
			))}
		</dl>
	);
}

/** `active` 时每 `ms` 毫秒返回一次新的当前时刻（运行中的时长随之走动）。 */
export function useTicker(active: boolean, ms = 1000): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (!active) return;
		setNow(Date.now());
		const timer = setInterval(() => setNow(Date.now()), ms);
		return () => clearInterval(timer);
	}, [active, ms]);
	return now;
}

/**
 * 就地确认条：一句后果说明 + 「保留 / 确认」两个按钮，替代原操作按钮出现在同一处（不弹二级对话框）。
 * 破坏性操作的确认按钮用 danger，其余用 secondary。窄屏说明独占一行、按钮靠右另起一行；出现时滚进视野。
 */
export function ConfirmStrip({
	label,
	body,
	cancelLabel,
	confirmLabel,
	danger,
	busy,
	onCancel,
	onConfirm,
	className = "",
}: {
	/** 读屏用的分组名 */
	label: string;
	body: ReactNode;
	cancelLabel: string;
	confirmLabel: string;
	danger?: boolean;
	busy?: boolean;
	onCancel: () => void;
	onConfirm: () => void;
	className?: string;
}): ReactNode {
	const ref = useRef<HTMLFieldSetElement>(null);
	// 确认条可能出现在对话框正文的可视区外（例如列表底部的账号行）
	useEffect(() => ref.current?.scrollIntoView?.({ block: "nearest" }), []);
	return (
		<fieldset
			ref={ref}
			aria-label={label}
			className={`flex min-w-0 flex-col gap-2 rounded-md bg-inset px-3 py-2.5 sm:flex-row sm:items-center sm:gap-3 ${className}`}
		>
			<p className="min-w-0 flex-1 text-muted text-xs">{body}</p>
			<div className="flex shrink-0 justify-end gap-2">
				<Button size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
					{cancelLabel}
				</Button>
				<Button
					size="sm"
					variant={danger ? "danger" : "secondary"}
					disabled={busy}
					onClick={onConfirm}
				>
					{busy && <Spinner size={12} />}
					{confirmLabel}
				</Button>
			</div>
		</fieldset>
	);
}

/** 当天时刻（时:分:秒），用于任务开始时间。 */
export function formatClock(ts: number): string {
	return new Intl.DateTimeFormat(i18n.language, {
		timeStyle: "medium",
	}).format(ts);
}
