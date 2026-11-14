/**
 * 应用内通知 toast（M5 计划 6.3）：右上角玻璃卡片，6s 自动消失（计时在通知 store 里）。
 * 点正文 → 跳到该线程并标为已读；× 只关闭。挂在 AppShell。
 * 最上面一张的右上角与屏幕圆角同心（`.dc-tr`，离边 12px），其余保持令牌圆角。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import {
	type AppNotification,
	notificationsStore,
	notificationText,
	useNotifications,
} from "../../state/notifications";
import { Icon } from "../ui/icons";
import { IconButton } from "../ui/primitives";
import { KIND_ICON } from "./NotificationBell";

function Toast({ n }: { n: AppNotification }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const { title, body } = notificationText(n, t);
	const kind = KIND_ICON[n.kind];
	const store = notificationsStore.getState();
	return (
		<div
			role="status"
			className="glass pop-in dc-tr pointer-events-auto flex items-start gap-1 rounded-lg py-1.5 pr-1.5 pl-1 [--dc-gap:100vmax] [--dc-r:var(--r-lg)] first:[--dc-gap:0.75rem]"
		>
			<button
				type="button"
				className="flex min-w-0 flex-1 items-start gap-2.5 rounded-md px-2 py-1 text-left"
				onClick={() => {
					store.dismissToast(n.id);
					navigate(n.url);
					void store.markRead(n.threadId).catch(() => {});
				}}
			>
				<Icon
					name={kind.icon}
					size={16}
					className={`mt-0.5 shrink-0 ${kind.className}`}
					aria-label={t(`notify.kind.${n.kind}`)}
				/>
				<span className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="truncate font-medium text-fg text-sm">{title}</span>
					<span className="line-clamp-2 break-words text-muted text-xs">
						{body}
					</span>
				</span>
			</button>
			<IconButton
				icon="close"
				box="xs"
				size={13}
				label={t("common.close")}
				onClick={() => store.dismissToast(n.id)}
			/>
		</div>
	);
}

export function NotificationToasts(): ReactNode {
	const { t } = useTranslation();
	const toasts = useNotifications((s) => s.toasts);
	if (toasts.length === 0) return null;
	return (
		<section
			aria-label={t("notify.toastRegion")}
			className="pointer-events-none fixed top-3 right-3 z-[60] flex w-[min(22rem,calc(100vw-1.5rem))] flex-col gap-2 pt-safe pr-safe"
		>
			{toasts.map((n) => (
				<Toast key={n.id} n={n} />
			))}
		</section>
	);
}
