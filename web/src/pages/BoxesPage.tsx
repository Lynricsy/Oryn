/**
 * Boxes 总览 `/boxes`（M23 步骤 7.3）：没有 box 时是介绍页（`BoxesHero`），有 box 时是 box 列表与全部 box 的最近活动。
 * 页头右侧「新建 Box」只在有 box 时出现（介绍页自己有主按钮）。存在 app 却没有地址（未配置 portal_domain）时页顶提示。
 * 滚动区带 `data-testid="boxes-scroll"`（截图的溢出断言量它）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useBoxActivity, useBoxes } from "../api/box-queries";
import { BoxActivityList } from "../components/boxes/BoxActivityList";
import { BoxesHero } from "../components/boxes/BoxesHero";
import {
	BOX_STATE_TONE,
	boxStateText,
	lastDeployedApp,
} from "../components/boxes/box-model";
import { NewBoxDialog } from "../components/boxes/NewBoxDialog";
import {
	ChromeLead,
	WindowControls,
} from "../components/layout/WindowControls";
import { Callout, ListRow, Sheet, StatusDot } from "../components/ui/data";
import { Icon } from "../components/ui/icons";
import {
	Button,
	ErrorText,
	IconButton,
	Spinner,
} from "../components/ui/primitives";
import { formatRelative } from "../lib/format";
import { useBreakpoint } from "../lib/viewport";
import { uiStore, useSidebarShown, useSidebarToggleShown } from "../state/ui";

export function BoxesPage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const sidebarToggle = useSidebarToggleShown();
	const chromeLeft = !useSidebarShown();
	const boxes = useBoxes();
	const list = boxes.data?.boxes ?? [];
	const activity = useBoxActivity("all");
	const [creating, setCreating] = useState(false);
	const noUrl = list.some((b) => b.apps.some((a) => !a.url));

	let body: ReactNode;
	if (boxes.isPending)
		body = (
			<div className="flex justify-center pt-[12vh]">
				<Spinner size={20} />
			</div>
		);
	else if (boxes.isError) body = <ErrorText error={boxes.error} />;
	else if (list.length === 0)
		body = (
			<BoxesHero wide={bp === "expanded"} onCreate={() => setCreating(true)} />
		);
	else
		body = (
			<div className="mx-auto flex w-full max-w-3xl flex-col gap-8">
				{noUrl && <Callout tone="warn">{t("boxes.noUrl")}</Callout>}
				<section>
					<h2 className="mb-2 px-2 font-medium text-faint text-xs">
						{t("boxes.list.title")}
					</h2>
					<div data-testid="boxes-list" className="flex flex-col gap-0.5">
						{list.map((b) => {
							const last = lastDeployedApp(b);
							return (
								<ListRow
									key={b.id}
									onClick={() => navigate(`/boxes/${b.id}`)}
									leading={
										<StatusDot
											tone={BOX_STATE_TONE[b.state]}
											pulse={b.state === "starting" || b.state === "stopping"}
											label={boxStateText(b, t)}
										/>
									}
									title={<span className="truncate font-medium">{b.name}</span>}
									meta={
										<>
											<span>{b.type}</span>
											<span aria-hidden>·</span>
											<span>
												{t("boxes.list.apps", { count: b.apps.length })}
											</span>
											{last?.live && (
												<>
													<span aria-hidden>·</span>
													<span className="truncate">
														{last.name} {formatRelative(last.live.activatedAt)}
													</span>
												</>
											)}
										</>
									}
									trailing={
										<Icon
											name="chevronRight"
											size={14}
											className="text-faint"
										/>
									}
								/>
							);
						})}
					</div>
				</section>
				<section>
					<h2 className="mb-2 px-2 font-medium text-faint text-xs">
						{t("boxes.list.recent")}
					</h2>
					{activity.isPending ? (
						<Spinner />
					) : activity.isError ? (
						<ErrorText error={activity.error} />
					) : (
						<BoxActivityList items={activity.data.items} showBox />
					)}
				</section>
			</div>
		);

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
						onClick={() => uiStore.getState().toggleSidebar()}
					/>
				)}
				<Icon name="hardDrives" size={16} className="shrink-0 text-faint" />
				<h1 className="min-w-0 flex-1 truncate font-semibold text-sm">
					{t("boxes.title")}
				</h1>
				{list.length > 0 && (
					<Button
						variant="primary"
						size="sm"
						icon="plus"
						onClick={() => setCreating(true)}
					>
						{t("boxes.new.open")}
					</Button>
				)}
				<WindowControls side="right" className="-mr-1" />
			</header>
			<div
				data-testid="boxes-scroll"
				className="min-h-0 flex-1 overflow-y-auto px-3 pt-4 pb-safe sm:px-6 sm:pt-6"
			>
				{body}
			</div>
			{creating && <NewBoxDialog onClose={() => setCreating(false)} />}
		</Sheet>
	);
}
