/**
 * 查看器的状态面板（计划 M14 2.3），都用 `EmptyState`：加载中、`not_found`、已在回收站（剩余天数 + 恢复）、
 * 申请帧失败或其它错误（重试）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button, describeError, EmptyState, Spinner } from "../ui/primitives";

/** 「最近删除」保留的天数（与服务端 `KeepDeleted` 一致）。 */
export const KEEP_DELETED_DAYS = 30;
const DAY_MS = 86_400_000;

/** 回收站里还剩几天被清理（至少 0，向上取整）。 */
export function daysLeft(deletedAt: number, now = Date.now()): number {
	return Math.max(
		0,
		Math.ceil((deletedAt + KEEP_DELETED_DAYS * DAY_MS - now) / DAY_MS),
	);
}

export function ViewerLoading(): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			data-testid="artifact-viewer-loading"
			role="status"
			aria-label={t("artifacts.viewer.loading")}
			className="flex h-full items-center justify-center text-faint"
		>
			<Spinner size={18} />
		</div>
	);
}

export function ViewerNotFound(): ReactNode {
	const { t } = useTranslation();
	return (
		<EmptyState icon="shapes" title={t("artifacts.viewer.notFound")}>
			{t("artifacts.viewer.notFoundHint")}
		</EmptyState>
	);
}

export function ViewerDeleted({
	deletedAt,
	onRestore,
	restoring,
}: {
	deletedAt: number;
	onRestore(): void;
	restoring: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<EmptyState icon="trash" title={t("artifacts.viewer.deleted")}>
			<div className="flex flex-col items-center gap-3">
				<span>
					{t("artifacts.viewer.deletedDays", { count: daysLeft(deletedAt) })}
				</span>
				<Button
					size="sm"
					icon="restart"
					disabled={restoring}
					onClick={onRestore}
				>
					{t("artifacts.viewer.restore")}
				</Button>
			</div>
		</EmptyState>
	);
}

/** 申请帧失败或其它错误。 */
export function FrameErrorState({
	error,
	onRetry,
}: {
	error: unknown;
	onRetry(): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div data-testid="artifact-viewer-error" className="h-full">
			<EmptyState icon="alert" title={t("artifacts.viewer.failed")}>
				<div className="flex flex-col items-center gap-3">
					{error ? (
						<span className="break-words">{describeError(error, t)}</span>
					) : null}
					<Button size="sm" icon="refresh" onClick={onRetry}>
						{t("artifacts.viewer.retry")}
					</Button>
				</div>
			</EmptyState>
		</div>
	);
}
