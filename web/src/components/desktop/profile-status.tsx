/**
 * 浏览器 Profile 的共用展示：409 `profile_in_use` 的解析、持有者线程名、orb 上 Profile 状态的一句话描述，
 * 以及信息面板 orb 卡里的「浏览器 Profile」一行。
 */
import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, type Orb, type Schemas } from "../../api/client";
import { parseTitleCard } from "../../lib/title";
import { Badge, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";

export type ProfileHolder = Schemas["BrowserProfileHolder"];
export type OrbDesktopProfile = Schemas["OrbDesktopProfile"];

/** 冲突对话框需要的内容：Profile 名、id 与当前持有者。 */
export interface ProfileConflict {
	profileId: string;
	profileName: string;
	holder: ProfileHolder;
}

/** 409 `profile_in_use` 的错误体；其它错误返回 null。 */
export function profileConflictOf(e: unknown): ProfileConflict | null {
	if (!(e instanceof ApiError) || e.code !== "profile_in_use") return null;
	const body = e.body as Partial<Schemas["ProfileInUseConflict"]> | undefined;
	if (!body?.holder || typeof body.profileId !== "string") return null;
	return {
		profileId: body.profileId,
		profileName: body.profileName ?? body.profileId,
		holder: body.holder,
	};
}

/** 持有者的线程名（去掉标题卡图标）；没有线程时退到 orb 短 id。 */
export function holderTitle(holder: ProfileHolder, t: TFunction): string {
	const title = holder.threadTitle
		? parseTitleCard(holder.threadTitle).text
		: "";
	return (
		title || t("deskPanel.profile.otherOrb", { id: holder.orbId.slice(0, 12) })
	);
}

/** orb 上 Profile 状态的一句话（Desktop 面板与信息面板共用）及其色调。 */
export function profileStatusText(
	p: OrbDesktopProfile,
	t: TFunction,
): { text: string; tone: Tone } {
	switch (p.status) {
		case "attached":
			return { text: t("deskPanel.profile.attached"), tone: "ok" };
		case "readonly":
			return p.readonlyReason === "taken_over"
				? {
						text: t("deskPanel.profile.takenOver", {
							thread: p.takenOverBy?.threadTitle
								? parseTitleCard(p.takenOverBy.threadTitle).text
								: t("deskPanel.profile.anotherThread"),
						}),
						tone: "neutral",
					}
				: { text: t("deskPanel.profile.readonly"), tone: "neutral" };
		case "released":
			return { text: t("deskPanel.profile.released"), tone: "neutral" };
		case "in_use":
			return {
				text: p.heldBy
					? t("deskPanel.profile.inUseBy", { thread: holderTitle(p.heldBy, t) })
					: t("deskPanel.profile.inUse"),
				tone: "warn",
			};
		case "error":
			return {
				text: t("deskPanel.profile.error", {
					reason: profileErrorText(p.error, t),
				}),
				tone: "err",
			};
	}
}

/** orbd 装载失败的错误码 → 原因（未知码给通用说明）。 */
export function profileErrorText(code: string | undefined, t: TFunction) {
	switch (code) {
		case "invalid_archive":
		case "too_large":
		case "no_space":
			return t(`deskPanel.profileError.${code}`);
		default:
			return t("deskPanel.profileError.other");
	}
}

/** 信息面板 orb 卡里的「浏览器 Profile」一行；orb 没选 Profile 时不显示。 */
export function DesktopProfileLine({ orb }: { orb: Orb }): ReactNode {
	const { t } = useTranslation();
	const p = orb.desktop?.profile;
	if (!p) return null;
	const { text, tone } = profileStatusText(p, t);
	return (
		<div className="mt-3" data-testid="info-desktop-profile">
			<h4 className="mb-1 font-medium text-2xs text-faint">
				{t("deskPanel.profile.title")}
			</h4>
			<div className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs">
				<Icon name="browser" size={13} className="shrink-0 text-faint" />
				<span className="min-w-0 truncate font-medium text-fg">{p.name}</span>
				<Badge tone={tone} title={text}>
					{text}
				</Badge>
			</div>
		</div>
	);
}
