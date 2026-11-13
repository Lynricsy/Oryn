/**
 * Portal 访问弹层（计划 M4 5.2）：齿轮按钮打开。未公开时说明「仅你可访问」并提供限时公开（1h / 3h / 1d / 7d，
 * 点下后二次确认一行）；公开中显示到期时间与每 30s 刷新的剩余时间、续期四档（同一接口，从现在起算）与终止公开。
 * 开合照 `Menu`（外点 / Esc 关闭、`glass pop-in rounded-lg`）；compact 档改用 `Dialog`。
 * 桌面弹层是非模态的：打开时焦点移到第一个可操作项（不困住 Tab），Esc 关闭时焦点还给齿轮按钮。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { useEndPortalShare, useSetPortalShare } from "../../api/queries";
import { formatDateTime } from "../../lib/format";
import { focusablesIn } from "../../lib/modal";
import { useBreakpoint } from "../../lib/viewport";
import { Icon, type IconName } from "../ui/icons";
import { Button, Dialog, ErrorText, IconButton } from "../ui/primitives";
import { formatRemaining } from "./portal-url";

type Duration = Schemas["PortalShareDuration"];
type OrbPortal = Schemas["OrbPortal"];

const DURATIONS: readonly Duration[] = ["1h", "3h", "1d", "7d"];

/** 当前时间，每 `intervalMs` 刷新一次（剩余时间用）。 */
export function useNow(intervalMs: number): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		const timer = setInterval(() => setNow(Date.now()), intervalMs);
		return () => clearInterval(timer);
	}, [intervalMs]);
	return now;
}

/** 仍在公开期内的公开信息（到期但还没收到控制面的更新时按未公开处理）。 */
export function activeShare(
	portal: OrbPortal | undefined,
	now: number,
): Schemas["OrbPortalPublic"] | undefined {
	return portal?.public && portal.public.expiresAt > now
		? portal.public
		: undefined;
}

/** 弹层头：圆底图标 + 标题 + 说明。 */
function StateHead({
	icon,
	title,
	children,
}: {
	icon: IconName;
	title: string;
	children: ReactNode;
}) {
	return (
		<div className="flex items-start gap-2.5">
			<span className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-inset text-muted">
				<Icon name={icon} size={14} aria-hidden />
			</span>
			<div className="min-w-0">
				<div className="font-medium text-fg text-sm leading-7">{title}</div>
				<div className="text-muted text-xs">{children}</div>
			</div>
		</div>
	);
}

function DurationRow({
	title,
	labelKey,
	disabled,
	selected,
	onPick,
}: {
	title: string;
	labelKey: "portal.publishFor" | "portal.renewFor";
	disabled: boolean;
	selected?: Duration | null;
	onPick: (d: Duration) => void;
}) {
	const { t } = useTranslation();
	return (
		<fieldset className="mt-3">
			<legend className="mb-1.5 font-medium text-2xs text-faint">
				{title}
			</legend>
			<div className="grid grid-cols-4 gap-1.5">
				{DURATIONS.map((d) => (
					<Button
						key={d}
						size="sm"
						variant="secondary"
						className={`px-1 ${selected === d ? "bg-hover ring-1 ring-border-strong" : ""}`}
						aria-label={t(labelKey, { d: t(`portal.duration.${d}`) })}
						aria-pressed={selected === undefined ? undefined : selected === d}
						disabled={disabled}
						onClick={() => onPick(d)}
					>
						{t(`portal.duration.${d}`)}
					</Button>
				))}
			</div>
		</fieldset>
	);
}

/** 弹层正文：按公开与否切换两种状态。 */
export function AccessBody({
	orbId,
	portal,
}: {
	orbId: string;
	portal: OrbPortal;
}): ReactNode {
	const { t, i18n } = useTranslation();
	const now = useNow(30_000);
	const share = useSetPortalShare(orbId);
	const end = useEndPortalShare(orbId);
	// 选了时长、等待确认
	const [pending, setPending] = useState<Duration | null>(null);
	const pub = activeShare(portal, now);
	const busy = share.isPending || end.isPending;
	const publish = (duration: Duration) =>
		share.mutate(
			{ port: portal.port, duration },
			{ onSuccess: () => setPending(null) },
		);

	if (pub)
		return (
			<div data-testid="portal-access" data-state="public">
				<StateHead icon="globe" title={t("portal.publicTitle")}>
					{t("portal.publicHint")}
				</StateHead>
				<div className="mt-3 flex items-center gap-2 rounded-md border border-border bg-inset px-2.5 py-2 text-xs">
					<Icon
						name="timer"
						size={13}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<span className="min-w-0 flex-1 truncate text-muted">
						{t("portal.expiresAt", { at: formatDateTime(pub.expiresAt) })}
					</span>
					<span
						data-testid="portal-remaining"
						className="shrink-0 font-medium text-fg tabular-nums"
					>
						{t("portal.remaining", {
							left: formatRemaining(pub.expiresAt - now, i18n.language),
						})}
					</span>
				</div>
				<DurationRow
					title={t("portal.renewTitle")}
					labelKey="portal.renewFor"
					disabled={busy}
					onPick={publish}
				/>
				<div className="mt-3 flex justify-end">
					<Button
						size="sm"
						variant="danger"
						icon="stop"
						disabled={busy}
						onClick={() => end.mutate({ port: portal.port })}
					>
						{t("portal.end")}
					</Button>
				</div>
				<div className="mt-2 empty:hidden">
					<ErrorText error={share.error ?? end.error} />
				</div>
			</div>
		);

	return (
		<div data-testid="portal-access" data-state="private">
			<StateHead icon="lock" title={t("portal.private")}>
				{t("portal.privateHint")}
			</StateHead>
			<DurationRow
				title={t("portal.publishTitle")}
				labelKey="portal.publishFor"
				disabled={busy}
				selected={pending}
				onPick={setPending}
			/>
			{pending && (
				<div className="mt-3 rounded-md border border-border bg-inset px-2.5 py-2">
					<p className="text-muted text-xs">
						{t("portal.confirmPublish", {
							port: portal.port,
							d: t(`portal.duration.${pending}`),
						})}
					</p>
					<div className="mt-2 flex justify-end gap-1.5">
						<Button
							size="sm"
							variant="ghost"
							disabled={busy}
							onClick={() => setPending(null)}
						>
							{t("common.cancel")}
						</Button>
						<Button
							size="sm"
							variant="primary"
							icon="globe"
							disabled={busy}
							onClick={() => publish(pending)}
						>
							{t("portal.confirm")}
						</Button>
					</div>
				</div>
			)}
			<div className="mt-2 empty:hidden">
				<ErrorText error={share.error} />
			</div>
		</div>
	);
}

/** 齿轮按钮 + 访问弹层。 */
export function AccessPopover({
	orbId,
	portal,
}: {
	orbId: string;
	portal: OrbPortal;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const [open, setOpen] = useState(false);
	const ref = useRef<HTMLDivElement>(null);
	const triggerRef = useRef<HTMLButtonElement>(null);
	const popRef = useRef<HTMLDivElement>(null);
	const compact = bp === "compact";
	// compact 档是 Dialog（自带焦点移入与还原），这里只管桌面弹层
	useLayoutEffect(() => {
		const pop = popRef.current;
		if (open && !compact && pop) (focusablesIn(pop)[0] ?? pop).focus();
	}, [open, compact]);
	useEffect(() => {
		// compact 档是 Dialog（自带遮罩与 Esc），这里只管弹层
		if (!open || compact) return;
		const onDown = (e: MouseEvent) => {
			// 点外面：焦点跟着用户点的位置走，不拉回齿轮
			if (ref.current && !ref.current.contains(e.target as Node))
				setOpen(false);
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			// 焦点在弹层里（或已随卸载的按钮掉到 body）时还给齿轮；用户已把焦点移到别处就不抢
			const active = document.activeElement;
			const restore =
				!active ||
				active === document.body ||
				(ref.current?.contains(active) ?? false);
			setOpen(false);
			if (restore) triggerRef.current?.focus();
		};
		document.addEventListener("mousedown", onDown);
		window.addEventListener("keydown", onKey);
		return () => {
			document.removeEventListener("mousedown", onDown);
			window.removeEventListener("keydown", onKey);
		};
	}, [open, compact]);
	const title = t("portal.accessTitle", { port: portal.port });
	return (
		<div ref={ref} className="relative">
			<IconButton
				ref={triggerRef}
				icon="settings"
				box="sm"
				size={15}
				label={t("portal.access")}
				aria-expanded={open}
				aria-haspopup="dialog"
				className={open ? "bg-active text-fg" : ""}
				onClick={() => setOpen((v) => !v)}
			/>
			{open &&
				(compact ? (
					<Dialog open title={title} onClose={() => setOpen(false)}>
						<AccessBody orbId={orbId} portal={portal} />
					</Dialog>
				) : (
					<div
						ref={popRef}
						role="dialog"
						aria-label={title}
						tabIndex={-1}
						className="glass pop-in outline-none absolute top-full right-0 z-40 mt-1 w-80 max-w-[calc(100vw-1rem)] rounded-lg p-3"
					>
						<h2 className="mb-2.5 font-medium text-faint text-xs">{title}</h2>
						<AccessBody orbId={orbId} portal={portal} />
					</div>
				))}
		</div>
	);
}
