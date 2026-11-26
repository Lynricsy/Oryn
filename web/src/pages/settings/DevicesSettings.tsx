/**
 * 设置页「设备」（default export，由设置页接入）：批准配对、设备列表与吊销、浏览器中继在线数、「用手机扫码连接」二维码。
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
import { Badge, Card, ListRow } from "../../components/ui/data";
import type { IconName } from "../../components/ui/icons";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import { confirmAction } from "../../lib/confirm";
import { formatRelative } from "../../lib/format";
import { host } from "../../lib/host";
import { pairUri, qrModules } from "../../lib/pair-qr";
import { LeadIcon, RowList } from "./parts";

/**
 * 「用手机扫码连接」：二维码内容 `oryn://pair?server=<本站 origin>`，手机 App 连接页扫码后只填服务器地址。
 * 二维码要高对比才扫得出：浅色主题用纸面底 + 正文色码，深色主题反过来（正文色作底、纸面色作码），始终浅底深码。
 */
function PairQrCard() {
	const { t } = useTranslation();
	const origin = window.location.origin;
	const uri = pairUri(origin);
	const qr = useMemo(() => qrModules(uri), [uri]);
	return (
		<Card title={t("devices.pairQr.title")} icon="qr">
			<div className="flex flex-col items-center gap-4 sm:flex-row sm:items-center">
				<div className="shrink-0 rounded-md bg-panel p-4 text-fg ring-1 ring-border [[data-theme=dark]_&]:bg-fg [[data-theme=dark]_&]:text-panel">
					<svg
						data-testid="pair-qr"
						data-qr-payload={uri}
						role="img"
						aria-label={t("devices.pairQr.label", { server: origin })}
						viewBox={`0 0 ${qr.size} ${qr.size}`}
						shapeRendering="crispEdges"
						className="block h-40 w-40"
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
				<div className="flex min-w-0 flex-col gap-1.5">
					<p className="break-all font-mono text-sm">{origin}</p>
					<p className="text-muted text-xs">{t("devices.pairQr.hint")}</p>
				</div>
			</div>
		</Card>
	);
}

const DEVICE_ICON: Record<Schemas["Device"]["kind"], IconName> = {
	cli: "terminal",
	desktop: "monitor",
	mobile: "device",
	browser_extension: "globe",
};

export default function DevicesSettings() {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const devices = useDevices();
	const me = useMe();
	const revoke = useAsync();
	const relayClients = useBrowserRelayClients();
	// App 会话（设备令牌换来的网页会话）不能批准配对：设备令牌不能再签发设备
	const appSession = me.data?.auth.kind === "web" && !!me.data.auth.deviceId;
	return (
		<>
			<Card title={t("devices.approveTitle")} icon="key">
				{appSession ? (
					<p className="text-muted text-xs">
						{t("devices.pairInBrowser")}{" "}
						<a
							href="/pair"
							target="_blank"
							rel="noopener noreferrer"
							className="text-accent-ink underline underline-offset-3"
						>
							{t("devices.pairInBrowserLink")}
						</a>
					</p>
				) : (
					<>
						<p className="mb-3 text-muted text-xs">{t("pair.hint")}</p>
						<PairApproval />
					</>
				)}
			</Card>
			<Card title={t("devices.title")} icon="device">
				{devices.isPending && <Spinner />}
				<ErrorText error={devices.error ?? revoke.error} />
				<RowList>
					{devices.data?.devices.map((d) => (
						<li key={d.id}>
							<ListRow
								leading={<LeadIcon name={DEVICE_ICON[d.kind] ?? "device"} />}
								title={
									<>
										<span className="truncate font-medium">{d.name}</span>
										{d.current && (
											<Badge tone="accent">{t("devices.current")}</Badge>
										)}
										{d.revokedAt && (
											<Badge tone="err">{t("devices.revoked")}</Badge>
										)}
									</>
								}
								meta={
									<>
										<Badge>{t(`devices.kind.${d.kind}`)}</Badge>
										{d.platform && <Badge>{d.platform}</Badge>}
										<span>
											{t("devices.lastSeen", {
												when: d.lastSeenAt ? formatRelative(d.lastSeenAt) : "—",
											})}
										</span>
									</>
								}
								trailing={
									!d.revokedAt && (
										<Button
											size="sm"
											variant="danger"
											disabled={revoke.busy}
											onClick={async () => {
												if (
													!(await confirmAction({
														title: t("devices.revokeConfirm", { name: d.name }),
														action: t("common.revoke"),
														danger: true,
													}))
												)
													return;
												void revoke
													.run(() =>
														unwrap(
															api.DELETE("/api/v1/devices/{id}", {
																params: { path: { id: d.id } },
															}),
														),
													)
													.then(() =>
														qc.invalidateQueries({ queryKey: qk.devices }),
													);
											}}
										>
											{t("devices.revoke")}
										</Button>
									)
								}
							/>
						</li>
					))}
				</RowList>
				{devices.data?.devices.length === 0 && (
					<p className="text-faint text-sm">{t("devices.empty")}</p>
				)}
			</Card>
			<Card title={t("devices.browserRelay.title")} icon="globe">
				<p className="text-muted text-xs">{t("devices.browserRelay.hint")}</p>
				<div className="mt-2 flex items-center gap-2 text-xs">
					<Badge
						tone={
							(relayClients.data?.clients.length ?? 0) > 0 ? "ok" : "neutral"
						}
					>
						{(relayClients.data?.clients.length ?? 0) > 0
							? t("devices.browserRelay.onlineCount", {
									count: relayClients.data?.clients.length ?? 0,
								})
							: t("devices.browserRelay.noneOnline")}
					</Badge>
				</div>
			</Card>
			{host.mobile === null && <PairQrCard />}
		</>
	);
}
