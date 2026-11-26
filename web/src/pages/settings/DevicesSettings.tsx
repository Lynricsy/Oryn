/**
 * 设置 → 账户与设备里的「设备」两组（default export，由 AccountSettings 接入）：
 * 1. 设备：已配对设备列表，行尾悬停出现「吊销」。
 * 2. 配对新设备：批准配对（输入配对码）、用手机扫码连接（二维码）、本机浏览器扩展在线数。
 * 桌面壳里的会话是设备换来的 App 会话：不能批准配对（改为引导到浏览器）。
 * 二维码只在浏览器与桌面壳里出现（手机自己不用扫自己）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useBrowserRelayClients } from "../../api/browser-relay-queries";
import { api, type Schemas, unwrap } from "../../api/client";
import { qk, useDevices, useMe } from "../../api/queries";
import { PairApproval } from "../../components/PairApproval";
import { useAsync } from "../../components/settings/useAsync";
import { Badge } from "../../components/ui/data";
import type { IconName } from "../../components/ui/icons";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
	Reveal,
	StatusText,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";
import { host } from "../../lib/host";
import { pairUri, qrModules } from "../../lib/pair-qr";

/** 行说明里的分隔点 */
const SEP = (
	<span aria-hidden className="text-faint">
		·
	</span>
);

/**
 * 「用手机扫码连接」的二维码：内容 `oryn://pair?server=<本站 origin>`，手机 App 连接页扫码后只填服务器地址。
 * 二维码要高对比才扫得出：浅色主题用纸面底 + 正文色码，深色主题反过来（正文色作底、纸面色作码），始终浅底深码。
 */
function PairQr() {
	const { t } = useTranslation();
	const origin = window.location.origin;
	const uri = pairUri(origin);
	const qr = useMemo(() => qrModules(uri), [uri]);
	return (
		<div className="flex flex-col items-start gap-2">
			<div className="shrink-0 rounded-[12px] bg-panel p-3 text-fg ring-1 ring-border [[data-theme=dark]_&]:bg-fg [[data-theme=dark]_&]:text-panel">
				<svg
					data-testid="pair-qr"
					data-qr-payload={uri}
					role="img"
					aria-label={t("devices.pairQr.label", { server: origin })}
					viewBox={`0 0 ${qr.size} ${qr.size}`}
					shapeRendering="crispEdges"
					className="block h-36 w-36"
				>
					{qr.runs.map((r) => (
						<rect
							key={`${r.x},${r.y}`}
							x={r.x}
							y={r.y}
							width={r.w}
							height={1}
							fill="currentColor"
						/>
					))}
				</svg>
			</div>
			<p className="break-all font-mono text-[12.5px] text-muted">{origin}</p>
		</div>
	);
}

const DEVICE_ICON: Record<Schemas["Device"]["kind"], IconName> = {
	cli: "terminal",
	desktop: "monitor",
	mobile: "device",
	browser_extension: "globe",
};

function DeviceList() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const devices = useDevices();
	const revoke = useAsync();
	const list = devices.data?.devices ?? [];
	const onRevoke = async (d: Schemas["Device"]) => {
		if (
			!(await confirmAction({
				title: t("devices.revokeConfirm", { name: d.name }),
				action: t("common.revoke"),
				danger: true,
			}))
		)
			return;
		const ok = await revoke.run(() =>
			unwrap(
				api.DELETE("/api/v1/devices/{id}", {
					params: { path: { id: d.id } },
				}),
			),
		);
		if (ok) await qc.invalidateQueries({ queryKey: qk.devices });
	};
	return (
		<Group title={t("devices.title")}>
			{devices.isPending ? (
				<GroupPanel>
					<Spinner />
				</GroupPanel>
			) : list.length === 0 && !devices.error ? (
				<GroupPanel>
					<p className="text-faint text-sm">{t("devices.empty")}</p>
				</GroupPanel>
			) : (
				list.length > 0 && (
					<GroupList>
						{list.map((d) => (
							<GroupRow
								key={d.id}
								icon={DEVICE_ICON[d.kind] ?? "device"}
								title={
									<>
										<span className="truncate">{d.name}</span>
										{d.current && (
											<Badge tone="accent">{t("devices.current")}</Badge>
										)}
									</>
								}
								meta={
									<>
										<span>{t(`devices.kind.${d.kind}`)}</span>
										{d.platform && (
											<>
												{SEP}
												<span>{d.platform}</span>
											</>
										)}
										{SEP}
										<span>
											{t("devices.lastSeen", {
												when: d.lastSeenAt ? formatRelative(d.lastSeenAt) : "—",
											})}
										</span>
									</>
								}
								trailing={
									d.revokedAt ? (
										<StatusText tone="err">{t("devices.revoked")}</StatusText>
									) : (
										<Reveal>
											<Button
												size="sm"
												variant="danger"
												disabled={revoke.busy}
												onClick={() => void onRevoke(d)}
											>
												{t("devices.revoke")}
											</Button>
										</Reveal>
									)
								}
							/>
						))}
					</GroupList>
				)
			)}
			<ErrorText error={devices.error ?? revoke.error} />
		</Group>
	);
}

function PairGroup() {
	const { t } = useTranslation();
	const me = useMe();
	const relayClients = useBrowserRelayClients();
	// App 会话（设备令牌换来的网页会话）不能批准配对：设备令牌不能再签发设备
	const appSession = me.data?.auth.kind === "web" && !!me.data.auth.deviceId;
	const online = relayClients.data?.clients.length ?? 0;
	return (
		<Group title={t("devices.pairTitle")}>
			<GroupList>
				{appSession ? (
					<GroupRow
						icon="key"
						title={t("devices.approveTitle")}
						meta={t("devices.pairInBrowser")}
						trailing={
							<a
								href="/pair"
								target="_blank"
								rel="noopener noreferrer"
								className="text-[12.5px] text-accent-ink underline underline-offset-3"
							>
								{t("devices.pairInBrowserLink")}
							</a>
						}
					/>
				) : (
					<GroupRow
						icon="key"
						title={t("devices.approveTitle")}
						meta={t("pair.hint")}
					>
						<PairApproval />
					</GroupRow>
				)}
				{host.mobile === null && (
					<GroupRow
						icon="qr"
						title={t("devices.pairQr.title")}
						meta={t("devices.pairQr.hint")}
					>
						<PairQr />
					</GroupRow>
				)}
				<GroupRow
					icon="browser"
					title={t("devices.browserRelay.title")}
					meta={t("devices.browserRelay.hint")}
					trailing={
						<StatusText tone={online > 0 ? "ok" : "neutral"}>
							{online > 0
								? t("devices.browserRelay.onlineCount", { count: online })
								: t("devices.browserRelay.noneOnline")}
						</StatusText>
					}
				/>
			</GroupList>
		</Group>
	);
}

export default function DevicesSettings() {
	return (
		<>
			<DeviceList />
			<PairGroup />
		</>
	);
}
