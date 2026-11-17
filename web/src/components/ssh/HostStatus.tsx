/**
 * SSH 主机（M18）的共享小件：最近一次测试的状态点、主机目标串。设置页、项目页卡片与线程右栏共用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { SshHost } from "../../api/ssh-queries";
import { formatDateTime } from "../../lib/format";
import { StatusDot } from "../ui/data";

/** 最近一次测试：成功 ok、失败 err、未测 neutral；悬停给时间与错误。 */
export function HostStatusDot({
	test,
}: {
	test: SshHost["lastTest"];
}): ReactNode {
	const { t } = useTranslation();
	const label = !test
		? t("sshPage.status.untested")
		: test.ok
			? t("sshPage.status.ok", { at: formatDateTime(test.at) })
			: t("sshPage.status.failed", {
					at: formatDateTime(test.at),
					error: test.error ?? "",
				});
	return (
		<span title={label} className="inline-flex">
			<StatusDot
				tone={!test ? "neutral" : test.ok ? "ok" : "err"}
				label={label}
			/>
		</span>
	);
}
