/**
 * M27 客户端浏览器中继（本机 Chrome）线程控制组件：
 * - `BrowserRelayChip`：挂在工作线程页头，展示绑定/在线/共享标签页数与待审批徽章，点击可切换绑定或解绑；
 * - `BrowserRelayApprovalBanner`：挂在输入框上方，存在待审批操作时提醒用户去 Chrome 扩展确认。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	useBindThreadBrowserRelay,
	useBrowserRelayClients,
	useThreadBrowserRelay,
	useUnbindThreadBrowserRelay,
} from "../api/browser-relay-queries";
import { Callout, StatusDot } from "./ui/data";
import { Icon } from "./ui/icons";
import { Menu, type MenuItem } from "./ui/primitives";

export function BrowserRelayChip({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const stateQ = useThreadBrowserRelay(threadId);
	const clientsQ = useBrowserRelayClients();
	const bind = useBindThreadBrowserRelay(threadId);
	const unbind = useUnbindThreadBrowserRelay(threadId);

	const state = stateQ.data;
	const clients = clientsQ.data?.clients ?? [];

	// 既未绑定又无在线扩展时保持零视觉噪音
	if (!state?.bound && clients.length === 0) return null;

	const busy = bind.isPending || unbind.isPending;
	const bound = state?.bound ?? false;
	const online = state?.online ?? false;
	const sharedTabs = state?.sharedTabCount ?? 0;
	const pending = state?.pendingApprovals ?? 0;
	const deviceName = state?.deviceName ?? state?.deviceId ?? "Chrome";

	const items: MenuItem[] = [];

	for (const c of clients) {
		const isCurrent = bound && state?.deviceId === c.deviceId;
		items.push({
			key: `bind:${c.deviceId}`,
			label: isCurrent
				? `${c.name} ✓`
				: t("browserRelay.bindTo", { name: c.name }),
			icon: "globe",
			disabled: busy || isCurrent,
			onSelect: () => {
				bind.mutate(c.deviceId);
			},
		});
	}
	if (clients.length === 0) {
		items.push({
			key: "empty",
			label: t("browserRelay.noClients"),
			disabled: true,
			onSelect: () => {},
		});
	}
	if (bound) {
		items.push({
			key: "unbind",
			label: t("browserRelay.unbind"),
			icon: "close",
			danger: true,
			disabled: busy,
			onSelect: () => {
				unbind.mutate();
			},
		});
	}

	const title = bound
		? online
			? t("browserRelay.boundOnline", { name: deviceName, tabs: sharedTabs })
			: t("browserRelay.boundOffline", { name: deviceName })
		: t("browserRelay.unbound");

	const toneCls =
		pending > 0
			? "border-warn/40 bg-warn/10 text-warn hover:bg-warn/15"
			: bound && online
				? "border-ok/35 bg-ok/8 text-fg hover:bg-ok/12"
				: "border-border bg-panel/60 text-muted hover:bg-hover";

	return (
		<Menu
			align="right"
			items={items}
			trigger={(props) => (
				<button
					{...props}
					type="button"
					data-testid="browser-relay-chip"
					aria-label={title}
					title={title}
					className={`ml-1.5 inline-flex h-[26px] shrink-0 items-center gap-1.5 rounded-full border px-2 font-medium text-xs transition-colors duration-150 ${toneCls}`}
				>
					<Icon name="globe" size={13} />
					{bound && (
						<StatusDot
							tone={pending > 0 ? "warn" : online ? "ok" : "neutral"}
						/>
					)}
					<span className="hidden @lg/header:inline">
						{bound
							? pending > 0
								? t("browserRelay.pendingBadge", { count: pending })
								: `${deviceName}${online ? ` · ${sharedTabs}` : ""}`
							: t("browserRelay.chipLabel")}
					</span>
				</button>
			)}
		/>
	);
}

export function BrowserRelayApprovalBanner({
	threadId,
}: {
	threadId: string;
}): ReactNode {
	const { t } = useTranslation();
	const stateQ = useThreadBrowserRelay(threadId);
	const state = stateQ.data;
	if (!state?.bound || !state.online || state.pendingApprovals <= 0) {
		return null;
	}
	const name = state.deviceName ?? state.deviceId ?? "Chrome";
	return (
		<div data-testid="browser-relay-approval-banner">
			<Callout tone="warn" role="status">
				{t("browserRelay.approvalBanner", {
					name,
					count: state.pendingApprovals,
				})}
			</Callout>
		</div>
	);
}
