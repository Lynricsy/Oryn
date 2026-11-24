/**
 * 设置 → 通知 → 此设备（移动壳，M7 计划 4.7；M28 官方推送网关）：系统推送由壳注册（UnifiedPush，或经官方推送网关的
 * FCM / APNs），页面只呈现壳归并后的状态并给出下一步。
 * - disabled：说明 + 启用；no_distributor（foss 包或无 Google 服务的设备）：装 UnifiedPush 分发器的说明 + 重试；
 * - permission_denied：打开系统通知设置；registering：转圈；error：原因（`gateway_*` 各有专属文案）+ 重试；
 * - active：当前通道（网关 FCM / APNs，或 UnifiedPush 分发器与端点主机）、关闭推送、发送测试通知；
 * - 其它状态（bridge ≤ 8 的旧壳会报 `unsupported`）：一句说明。
 * 壳给出 `alt`（gms 包可切换到的另一通道）且支持切换（bridge ≥ 9）时，active / no_distributor / error 下另有切换按钮。
 * 挂载时取 `mobile.info()`，壳推来 `mobile` 事件（推送状态变了）后重取；按钮调用期间禁用，失败时显示错误。
 */
import { type ReactNode, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Callout, Card } from "../../components/ui/data";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import {
	host,
	type MobileHost,
	type MobileInfo,
	type MobilePushTransport,
} from "../../lib/host";

/** UnifiedPush 分发器 ntfy 的安装说明。 */
export const NTFY_GUIDE = "https://unifiedpush.org/users/distributors/ntfy/";

/** 有专属文案的推送错误码（docs/plan-m28.md K6 与 orynd 透传的 gateway_unreachable）；其余沿用通用错误文案。 */
const PUSH_ERRORS = [
	"gateway_disabled",
	"gateway_unsupported",
	"gateway_untrusted",
	"gateway_app_mismatch",
	"gateway_challenge_timeout",
	"gateway_rejected",
	"gateway_unreachable",
] as const;
type PushErrorCode = (typeof PUSH_ERRORS)[number];

/** 错误的码：整串，或 `<码>: <详情>` 的前缀。 */
function pushErrorCode(error: string | null | undefined): PushErrorCode | null {
	const code = error?.split(/[:\s]/, 1)[0] ?? "";
	return (PUSH_ERRORS as readonly string[]).includes(code)
		? (code as PushErrorCode)
		: null;
}

export default function MobilePushCard(): ReactNode {
	const mobile = host.mobile;
	return mobile ? <PushCard mobile={mobile} /> : null;
}

function PushCard({ mobile }: { mobile: MobileHost }) {
	const { t } = useTranslation();
	const [info, setInfo] = useState<MobileInfo | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [sent, setSent] = useState(false);

	const refresh = useCallback(() => {
		mobile.info().then(
			(i) => {
				setInfo(i);
				setError(null);
			},
			(e) => setError(e),
		);
	}, [mobile]);

	useEffect(() => {
		refresh();
		return host.onMobile(refresh);
	}, [refresh]);

	/** 启用 / 关闭推送：壳返回新的状态。 */
	const act = async (fn: () => Promise<MobileInfo>) => {
		setBusy(true);
		setError(null);
		try {
			setInfo(await fn());
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	const push = info?.push;
	const state = push?.state ?? "";
	/** 当前通道对应的 transport：gms 包手动改用了 UnifiedPush（alt 为 fcm）时重试要沿用它，免得退回默认通道。 */
	const current: MobilePushTransport | undefined =
		push?.alt === "fcm"
			? "unifiedpush"
			: push?.alt === "unifiedpush"
				? "default"
				: undefined;
	const enable = () => void act(() => mobile.enablePush(current));
	const alt = mobile.pushTransport ? push?.alt : null;
	const switcher = alt ? (
		<Button
			size="sm"
			variant="ghost"
			disabled={busy}
			onClick={() =>
				void act(() =>
					mobile.enablePush(alt === "unifiedpush" ? "unifiedpush" : "default"),
				)
			}
			data-testid="mobile-push-switch"
		>
			{alt === "unifiedpush"
				? t("mobile.push.switchToUnifiedPush")
				: t("mobile.push.switchToGateway")}
		</Button>
	) : null;
	let body: ReactNode = <Spinner />;
	if (push) {
		switch (push.state) {
			case "disabled":
				body = (
					<>
						<p className="text-muted text-sm">{t("mobile.push.disabled")}</p>
						<div>
							<Button
								variant="primary"
								disabled={busy}
								onClick={enable}
								data-testid="mobile-push-enable"
							>
								{t("mobile.push.enable")}
							</Button>
						</div>
					</>
				);
				break;
			case "no_distributor":
				body = (
					<Callout
						tone="warn"
						actions={
							<>
								<Button
									size="sm"
									onClick={() => mobile.openExternal(NTFY_GUIDE)}
									data-testid="mobile-push-guide"
								>
									{t("mobile.push.installGuide")}
								</Button>
								<Button
									size="sm"
									disabled={busy}
									onClick={enable}
									data-testid="mobile-push-retry"
								>
									{t("mobile.push.retry")}
								</Button>
								{switcher}
							</>
						}
					>
						{t("mobile.push.noDistributor")}
					</Callout>
				);
				break;
			case "permission_denied":
				body = (
					<Callout
						tone="warn"
						actions={
							<Button
								size="sm"
								onClick={() => mobile.openNotificationSettings()}
								data-testid="mobile-push-settings"
							>
								{t("mobile.push.openSettings")}
							</Button>
						}
					>
						{t("mobile.push.permissionDenied")}
					</Callout>
				);
				break;
			case "registering":
				body = (
					<p className="inline-flex items-center gap-2 text-muted text-sm">
						<Spinner size={13} />
						{t("mobile.push.registering")}
					</p>
				);
				break;
			case "active": {
				const via =
					push.channel === "fcm"
						? t("mobile.push.viaFcm")
						: push.channel === "apns"
							? t("mobile.push.viaApns")
							: t("mobile.push.viaUnifiedPush", {
									distributor: push.distributor || "UnifiedPush",
									detail: ["UnifiedPush", push.endpointHost]
										.filter(Boolean)
										.join(" · "),
								});
				body = (
					<>
						<p className="text-sm" data-testid="mobile-push-via">
							{via}
						</p>
						<div className="flex flex-wrap items-center gap-2">
							<Button
								size="sm"
								onClick={() => {
									host.notify({
										id: `test-${Date.now()}`,
										title: t("notifySettings.device.testTitle"),
										body: t("notifySettings.device.testBody"),
										path: "/settings/notifications",
									});
									setSent(true);
								}}
								data-testid="mobile-push-test"
							>
								{t("notifySettings.device.testSend")}
							</Button>
							<Button
								size="sm"
								variant="ghost"
								disabled={busy}
								onClick={() => void act(() => mobile.disablePush())}
								data-testid="mobile-push-disable"
							>
								{t("mobile.push.disable")}
							</Button>
							{switcher}
							{sent && (
								<span role="status" className="text-faint text-xs">
									{t("notifySettings.device.testSent")}
								</span>
							)}
						</div>
					</>
				);
				break;
			}
			case "error": {
				const code = pushErrorCode(push.error);
				body = (
					<Callout
						tone="err"
						actions={
							<>
								<Button
									size="sm"
									disabled={busy}
									onClick={enable}
									data-testid="mobile-push-retry"
								>
									{t("mobile.push.retry")}
								</Button>
								{switcher}
							</>
						}
					>
						<span data-testid="mobile-push-error" data-code={code ?? ""}>
							{code
								? t(`mobile.push.errors.${code}`)
								: t("mobile.push.error", {
										error: push.error || t("mobile.push.errorUnknown"),
									})}
						</span>
					</Callout>
				);
				break;
			}
			default:
				body = <Callout tone="neutral">{t("mobile.push.unsupported")}</Callout>;
		}
	}

	return (
		<div
			data-testid="mobile-push-card"
			data-push-state={state}
			data-push-via={push?.channel ?? ""}
		>
			<Card title={t("notifySettings.device.title")} icon="phone">
				<div className="flex flex-col gap-3">
					{body}
					<ErrorText error={error} />
				</div>
			</Card>
		</div>
	);
}
