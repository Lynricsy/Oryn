/**
 * 单台 box `/boxes/:boxId`（M23 步骤 7.4）。页头：面包屑「Boxes / <名字>」、在机器上开线程的圆形主按钮、box 菜单
 * （启动 / 停止、改名、运行时段、删除）。主栏：APPS（`AppRow` + 行菜单）与活动流；侧栏（`expanded` 在右，其余排在
 * 主栏下方）：21 天用量与 About。box 不存在（`box_not_found`）时回到总览（分区记忆会记住路径，不能停在 404），
 * 其它错误原地显示并可重试。滚动区带 `data-testid="box-scroll"`。
 */
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import {
	type Box,
	type BoxApp,
	useBox,
	useBoxActivity,
	useBoxReleases,
	useBoxUsage,
	useCancelBoxDeploy,
	useDeleteBox,
	useRestartBoxApp,
	useRollbackBoxApp,
	useStartBox,
	useStopBox,
	useUndeployBoxApp,
	useUpdateBoxApp,
} from "../api/box-queries";
import { ApiError } from "../api/client";
import { canHoverPreview } from "../components/boxes/AppPreview";
import { AppRow } from "../components/boxes/AppRow";
import { BoxActivityList } from "../components/boxes/BoxActivityList";
import {
	RenameBoxDialog,
	ScheduleDialog,
	SecretsDialog,
} from "../components/boxes/BoxDialogs";
import { BoxUsageChart } from "../components/boxes/BoxUsageChart";
import {
	BOX_STATE_TONE,
	boxStateText,
	scheduleText,
} from "../components/boxes/box-model";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { ServiceLogDialog } from "../components/thread/ServicesSection";
import { CopyButton } from "../components/ui/CopyButton";
import { KeyValue, Sheet, StatusDot } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Menu,
	type MenuItem,
	Spinner,
} from "../components/ui/primitives";
import { confirmAction } from "../lib/confirm";
import {
	formatDateTime,
	formatOrbCpus,
	formatOrbMemory,
	formatRelative,
} from "../lib/format";
import { useBreakpoint } from "../lib/viewport";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

type BoxDialog =
	| { kind: "rename" }
	| { kind: "schedule" }
	| { kind: "secrets"; app: string }
	| { kind: "log"; service: string };

/** 确认后把执行位置交给首屏（机器：省略 app）。 */
async function startThread(
	box: Box,
	app: string | undefined,
	t: TFunction,
	go: () => void,
): Promise<void> {
	const ok = await confirmAction({
		title: t("boxes.confirm.thread.title", {
			target: app ? `${box.name} · ${app}` : box.name,
		}),
		body: t("boxes.confirm.thread.body"),
		action: t("boxes.confirm.thread.action"),
	});
	if (!ok) return;
	uiStore.getState().seedHome({
		text: "",
		executor: { kind: "box", boxId: box.id, ...(app ? { app } : {}) },
	});
	go();
}

/** 一个 app 行：菜单需要这个 app 的 release 列表（回滚目标），所以每行自己查。 */
function BoxAppItem({
	box,
	app,
	onDialog,
	onError,
}: {
	box: Box;
	app: BoxApp;
	onDialog: (d: BoxDialog) => void;
	onError: (e: unknown) => void;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const releases = useBoxReleases(box.id, app.name, Boolean(app.live));
	const setPublic = useUpdateBoxApp();
	const restart = useRestartBoxApp();
	const rollback = useRollbackBoxApp();
	const undeploy = useUndeployBoxApp();
	const cancel = useCancelBoxDeploy();
	const ref = { box: box.id, app: app.name };
	const opts = { onError };
	const stopped = box.desired === "stopped";
	const kept = (releases.data?.releases ?? []).filter(
		(r) => r.state === "kept",
	);
	const menu: MenuItem[] = app.live
		? [
				app.public
					? {
							key: "private",
							icon: "lock",
							label: t("boxes.apps.makePrivate"),
							onSelect: () => setPublic.mutate({ ...ref, public: false }, opts),
						}
					: {
							key: "public",
							icon: "globe",
							label: t("boxes.apps.makePublic"),
							onSelect: async () => {
								const ok = await confirmAction({
									title: t("boxes.confirm.public.title", { app: app.name }),
									body: t("boxes.confirm.public.body"),
									action: t("boxes.confirm.public.action"),
								});
								if (ok) setPublic.mutate({ ...ref, public: true }, opts);
							},
						},
				{
					key: "restart",
					icon: "restart",
					label: t("boxes.apps.restart"),
					onSelect: () => restart.mutate(ref, opts),
				},
				kept.length > 0
					? {
							key: "rollback",
							icon: "back",
							label: t("boxes.apps.rollback"),
							children: kept.map((r) => ({
								key: r.id,
								label: `${r.commit.slice(0, 7)} · ${r.branch}`,
								hint: formatRelative(r.activatedAt),
								native: { label: `${r.commit.slice(0, 7)} · ${r.branch}` },
								onSelect: () =>
									rollback.mutate({ ...ref, releaseId: r.id }, opts),
							})),
						}
					: {
							key: "rollback",
							icon: "back",
							label: t("boxes.apps.noRollback"),
							disabled: true,
						},
				{
					key: "secrets",
					icon: "key",
					label: t("boxes.apps.secrets"),
					onSelect: () => onDialog({ kind: "secrets", app: app.name }),
				},
				app.services.length > 1
					? {
							key: "logs",
							icon: "fileText",
							label: t("boxes.apps.logs"),
							children: app.services.map((s) => ({
								key: s.name,
								label: s.name.slice(app.name.length + 2) || s.name,
								native: { label: s.name },
								onSelect: () => onDialog({ kind: "log", service: s.name }),
							})),
						}
					: {
							key: "logs",
							icon: "fileText",
							label: t("boxes.apps.logs"),
							disabled: app.services.length === 0,
							onSelect: () => {
								const s = app.services[0];
								if (s) onDialog({ kind: "log", service: s.name });
							},
						},
				{
					key: "undeploy",
					icon: "minusCircle",
					label: t("boxes.apps.undeploy"),
					danger: true,
					onSelect: async () => {
						const ok = await confirmAction({
							title: t("boxes.confirm.undeploy.title", { app: app.name }),
							body: t("boxes.confirm.undeploy.body"),
							action: t("boxes.confirm.undeploy.action"),
							danger: true,
						});
						if (ok) undeploy.mutate(ref, opts);
					},
				},
			]
		: [
				{
					key: "cancel",
					icon: "close",
					label: t("boxes.apps.cancelDeploy"),
					onSelect: () => cancel.mutate(ref, opts),
				},
			];
	return (
		<AppRow
			app={app}
			menu={menu}
			preview={app.preview && Boolean(app.url) && canHoverPreview()}
			threadDisabled={
				stopped
					? t("boxes.apps.startFirst")
					: !app.live
						? t("boxes.apps.firstDeploy")
						: undefined
			}
			onNewThread={() =>
				void startThread(box, app.name, t, () => navigate("/"))
			}
		/>
	);
}

function BoxBody({
	box,
	error,
	onDialog,
	onError,
}: {
	box: Box;
	error: unknown;
	onDialog: (d: BoxDialog) => void;
	onError: (e: unknown) => void;
}): ReactNode {
	const { t } = useTranslation();
	const wide = useBreakpoint() === "expanded";
	const activity = useBoxActivity(box.id);
	const usage = useBoxUsage(box.id);
	const command = `oryn boxes deploy --on ${box.name} --name <app>`;
	return (
		<div
			className={`mx-auto grid w-full max-w-5xl gap-8 ${wide ? "grid-cols-[minmax(0,1fr)_18rem]" : ""}`}
		>
			<div className="flex min-w-0 flex-col gap-8">
				{error != null && <ErrorText error={error} />}
				<section>
					<h2 className="mb-2 px-2 font-medium font-mono text-2xs text-faint uppercase tracking-widest">
						{t("boxes.apps.title", { count: box.apps.length })}
					</h2>
					{box.apps.length === 0 ? (
						<EmptyState icon="hardDrives" title={t("boxes.apps.empty")}>
							<span className="mt-1 inline-flex max-w-full items-center gap-1 rounded-md bg-inset py-1 pr-1 pl-2.5">
								<code className="min-w-0 truncate font-mono text-fg text-xs">
									{command}
								</code>
								<CopyButton text={command} />
							</span>
						</EmptyState>
					) : (
						<div className="flex flex-col gap-0.5">
							{box.apps.map((a) => (
								<BoxAppItem
									key={a.name}
									box={box}
									app={a}
									onDialog={onDialog}
									onError={onError}
								/>
							))}
						</div>
					)}
				</section>
				<section>
					<h2 className="mb-2 px-2 font-medium font-mono text-2xs text-faint uppercase tracking-widest">
						{t("boxes.activity.title")}
					</h2>
					{activity.isPending ? (
						<Spinner />
					) : activity.isError ? (
						<ErrorText error={activity.error} />
					) : (
						<BoxActivityList items={activity.data.items} />
					)}
				</section>
			</div>
			<aside className="flex min-w-0 flex-col gap-6">
				{usage.data && <BoxUsageChart usage={usage.data} />}
				<section data-testid="box-about">
					<h2 className="mb-2 font-medium text-faint text-xs">
						{t("boxes.about.title")}
					</h2>
					<KeyValue
						rows={[
							{
								key: "status",
								label: t("boxes.about.status"),
								value: (
									<span className="inline-flex flex-wrap items-center gap-1.5">
										<StatusDot
											tone={BOX_STATE_TONE[box.state]}
											pulse={
												box.state === "starting" || box.state === "stopping"
											}
										/>
										{boxStateText(box, t)}
										{box.state === "failed" && box.failureReason && (
											<span className="text-faint text-xs">
												{box.failureReason}
											</span>
										)}
									</span>
								),
							},
							{
								key: "schedule",
								label: t("boxes.about.schedule"),
								value: scheduleText(box.schedule, t),
							},
							{
								key: "size",
								label: t("boxes.about.size"),
								value: t("boxes.about.sizeValue", {
									type: box.type,
									cpus: formatOrbCpus(box.cpus),
									memory: formatOrbMemory(box.memoryMB),
								}),
							},
							{
								key: "provider",
								label: t("boxes.about.provider"),
								value: box.provider,
							},
							{
								key: "created",
								label: t("boxes.about.created"),
								value: formatDateTime(box.createdAt),
							},
						]}
					/>
					<p className="mt-3 text-faint text-xs">{t("boxes.about.footnote")}</p>
				</section>
			</aside>
		</div>
	);
}

/** 页头右侧：在机器上开线程的圆形主按钮与 box 菜单。 */
function BoxHeaderActions({
	box,
	onDialog,
	onError,
}: {
	box: Box;
	onDialog: (d: BoxDialog) => void;
	onError: (e: unknown) => void;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const start = useStartBox();
	const stop = useStopBox();
	const del = useDeleteBox();
	const stopped = box.desired === "stopped";
	const items: MenuItem[] = [
		stopped
			? {
					key: "start",
					icon: "play",
					label: t("boxes.menu.start"),
					onSelect: () => start.mutate(box.id, { onError }),
				}
			: {
					key: "stop",
					icon: "pause",
					label: t("boxes.menu.stop"),
					onSelect: async () => {
						const ok = await confirmAction({
							title: t("boxes.confirm.stop.title", { name: box.name }),
							body: t("boxes.confirm.stop.body"),
							action: t("boxes.confirm.stop.action"),
						});
						if (ok) stop.mutate(box.id, { onError });
					},
				},
		{
			key: "rename",
			icon: "edit",
			label: t("boxes.menu.rename"),
			onSelect: () => onDialog({ kind: "rename" }),
		},
		{
			key: "schedule",
			icon: "clock",
			label: t("boxes.menu.schedule"),
			onSelect: () => onDialog({ kind: "schedule" }),
		},
		{
			key: "delete",
			icon: "trash",
			label: t("boxes.menu.delete"),
			danger: true,
			onSelect: async () => {
				const ok = await confirmAction({
					title: t("boxes.confirm.delete.title", { name: box.name }),
					body: t("boxes.confirm.delete.body"),
					action: t("boxes.confirm.delete.action"),
					danger: true,
				});
				if (ok)
					del.mutate(box.id, {
						onSuccess: () => navigate("/boxes", { replace: true }),
						onError,
					});
			},
		},
	];
	return (
		<>
			{/* 禁用的按钮不接指针事件：说明写在外层，悬停照样能看到 */}
			<span title={stopped ? t("boxes.apps.startFirst") : undefined}>
				<Button
					variant="primary"
					icon="plus"
					aria-label={t("boxes.apps.machineThread")}
					title={t("boxes.apps.machineThread")}
					disabled={stopped}
					className="w-8 rounded-full! px-0"
					onClick={() =>
						void startThread(box, undefined, t, () => navigate("/"))
					}
				/>
			</span>
			<Menu
				items={items}
				trigger={(p) => (
					<IconButton {...p} icon="more" label={t("boxes.menu.label")} />
				)}
			/>
		</>
	);
}

export function BoxPage(): ReactNode {
	const { t } = useTranslation();
	const { boxId = "" } = useParams();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	const box = useBox(boxId);
	const [dialog, setDialog] = useState<BoxDialog | null>(null);
	const [error, setError] = useState<unknown>(null);
	if (
		box.isError &&
		box.error instanceof ApiError &&
		box.error.code === "box_not_found"
	)
		return <Navigate to="/boxes" replace />;
	const close = () => setDialog(null);
	const secretsApp =
		dialog?.kind === "secrets"
			? box.data?.apps.find((a) => a.name === dialog.app)
			: undefined;
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<header
				data-window-drag=""
				className="flex h-12 shrink-0 items-center gap-2 border-border border-b px-3"
			>
				<ChromeLead show={chromeLeft} />
				{sidebarToggle && (
					<IconButton
						icon={bp === "expanded" ? "sidebar" : "menu"}
						label={t("nav.sidebar")}
						data-sidebar-toggle=""
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<Icon name="hardDrives" size={16} className="shrink-0 text-faint" />
				<h1 className="flex min-w-0 flex-1 items-center gap-2 font-semibold text-sm">
					<Link to="/boxes" className="shrink-0 hover:underline">
						{t("boxes.title")}
					</Link>
					<span aria-hidden className="text-ghost">
						/
					</span>
					<span className="truncate">{box.data?.name ?? boxId}</span>
				</h1>
				{box.data && (
					<BoxHeaderActions
						box={box.data}
						onDialog={setDialog}
						onError={setError}
					/>
				)}
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div
				data-testid="box-scroll"
				className="min-h-0 flex-1 overflow-y-auto px-3 pt-4 pb-safe sm:px-6 sm:pt-6"
			>
				{box.isPending ? (
					<div className="flex justify-center pt-[12vh]">
						<Spinner size={20} />
					</div>
				) : box.isError ? (
					<div className="flex flex-col items-start gap-2">
						<ErrorText error={box.error} />
						<Button size="sm" icon="refresh" onClick={() => void box.refetch()}>
							{t("actionError.retry")}
						</Button>
					</div>
				) : (
					<BoxBody
						box={box.data}
						error={error}
						onDialog={setDialog}
						onError={setError}
					/>
				)}
			</div>
			{box.data && dialog?.kind === "rename" && (
				<RenameBoxDialog box={box.data} onClose={close} />
			)}
			{box.data && dialog?.kind === "schedule" && (
				<ScheduleDialog box={box.data} onClose={close} />
			)}
			{box.data && secretsApp && (
				<SecretsDialog box={box.data} app={secretsApp} onClose={close} />
			)}
			{box.data && dialog?.kind === "log" && (
				<ServiceLogDialog
					orbId={box.data.orbId}
					name={dialog.service}
					onClose={close}
				/>
			)}
		</Sheet>
	);
}
