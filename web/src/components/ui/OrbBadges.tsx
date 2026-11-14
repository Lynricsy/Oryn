/**
 * M11a 的 orb 徽标：「Docker」（类型声明了 `docker` 特性 / orb 的 `nestedDocker`）与「预热」（预热池 orb，`orb.pool`）。
 * provider 类型列表、执行位置选择器、信息面板、项目页与统计页的 orb 列表共用，保证同一含义只有一种外观。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { Badge } from "./data";

/** 类型是否声明了 orb 内可用 Docker。 */
export function typeHasDocker(
	t: Pick<Schemas["OrbType"], "features">,
): boolean {
	return t.features?.includes("docker") ?? false;
}

export function DockerBadge(): ReactNode {
	const { t } = useTranslation();
	return (
		<Badge icon="docker" title={t("accel.dockerHint")}>
			{t("accel.docker")}
		</Badge>
	);
}

export function WarmBadge({
	pool,
}: {
	pool: Schemas["OrbPoolState"];
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Badge icon="warm" title={t(`accel.pool.${pool}`)}>
			{t("accel.warm")}
		</Badge>
	);
}
