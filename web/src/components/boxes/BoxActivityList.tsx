/**
 * Boxes 的活动流（M23 步骤 7.3 / 7.4）：每行一个图标、文案、相对时间；线程项链到 `/t/<id>`。
 * 总览页（`showBox`）在每行前面加 box 名；非主人做的事在文案前标出是谁（Agent / 助手 / 时段 / 系统）。
 */
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { BoxActivity } from "../../api/box-queries";
import { formatRelative } from "../../lib/format";
import { Icon, type IconName } from "../ui/icons";

const KIND_ICON: Record<BoxActivity["kind"], IconName> = {
	created: "plus",
	started: "play",
	stopped: "pause",
	renamed: "edit",
	schedule: "clock",
	deployed: "upload",
	deploy_failed: "alert",
	rolled_back: "back",
	restarted: "restart",
	undeployed: "minusCircle",
	visibility: "lock",
	thread: "message",
};

function activityText(a: BoxActivity, t: TFunction): string {
	switch (a.kind) {
		case "deployed":
			return t("boxes.activity.deployed", {
				app: a.app ?? "",
				commit: (a.commit ?? "").slice(0, 7),
			});
		case "visibility":
			return t("boxes.activity.visibility", {
				app: a.app ?? "",
				state: t(a.public ? "boxes.activity.public" : "boxes.activity.private"),
			});
		case "renamed":
			return t("boxes.activity.renamed", {
				from: a.from ?? "",
				to: a.to ?? "",
			});
		case "thread":
			return t("boxes.activity.thread", { title: a.title || a.threadId || "" });
		case "deploy_failed":
		case "rolled_back":
		case "restarted":
		case "undeployed":
			return t(`boxes.activity.${a.kind}`, { app: a.app ?? "" });
		default:
			return t(`boxes.activity.${a.kind}`);
	}
}

export function BoxActivityList({
	items,
	showBox = false,
}: {
	items: readonly BoxActivity[];
	showBox?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	if (items.length === 0)
		return (
			<p className="px-2 py-3 text-faint text-sm">
				{t("boxes.activity.empty")}
			</p>
		);
	return (
		<ul data-testid="box-activity" className="flex flex-col">
			{items.map((a) => {
				const text = activityText(a, t);
				return (
					<li
						key={a.id}
						className="flex min-h-9 items-start gap-2.5 rounded-md px-2 py-1.5 text-sm"
					>
						<Icon
							name={KIND_ICON[a.kind]}
							size={14}
							className={`mt-[3px] shrink-0 ${a.kind === "deploy_failed" ? "text-err" : "text-faint"}`}
						/>
						<span className="min-w-0 flex-1 text-fg">
							{showBox && (
								<Link
									to={`/boxes/${a.boxId}`}
									className="mr-1.5 font-medium hover:underline"
								>
									{a.boxName}
								</Link>
							)}
							{a.actor !== "owner" && (
								<span className="mr-1 text-faint">
									{t(`boxes.actor.${a.actor}`)}
								</span>
							)}
							{a.kind === "thread" && a.threadId ? (
								<Link to={`/t/${a.threadId}`} className="hover:underline">
									{text}
								</Link>
							) : (
								text
							)}
							{a.error && (
								<span className="mt-0.5 block truncate text-faint text-xs">
									{a.error}
								</span>
							)}
						</span>
						<time
							dateTime={new Date(a.ts).toISOString()}
							className="shrink-0 pt-px text-faint text-xs"
						>
							{formatRelative(a.ts)}
						</time>
					</li>
				);
			})}
		</ul>
	);
}
