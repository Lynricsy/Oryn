/** 分享链接的展示：中间省略的短 URL 与模式徽标（分享对话框与设置页总览共用）。 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ShareMode } from "../../api/share-queries";
import { formatDateTime } from "../../lib/format";
import { Badge } from "../ui/data";

/**
 * 去掉协议，把 slug（32 位十六进制）压成「前 6 位…后 4 位」：`oryn.example.com/s/1a2b3c…9f3c`。
 * 其它形态的路径段超过 24 字符时同样中间省略。
 */
export function shortShareUrl(url: string): string {
	const bare = url.replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
	return bare
		.split("/")
		.map((seg) =>
			seg.length > 24 ? `${seg.slice(0, 6)}…${seg.slice(-4)}` : seg,
		)
		.join("/");
}

/** 快照显示创建时间（「快照 · 11月13日 10:24」），实时只写「实时」。 */
export function ShareModeBadge({
	mode,
	createdAt,
}: {
	mode: ShareMode;
	createdAt: number;
}): ReactNode {
	const { t } = useTranslation();
	if (mode === "live")
		return <Badge icon="broadcast">{t("share.mode.live")}</Badge>;
	return (
		<Badge icon="camera" title={formatDateTime(createdAt)}>
			{t("share.page.snapshotBadge", { date: formatDateTime(createdAt) })}
		</Badge>
	);
}
