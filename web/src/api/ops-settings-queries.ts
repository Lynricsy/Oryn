/**
 * 控制面运维参数（设置 → 控制面）：读 `GET server/ops-settings`，改 `PATCH server/ops-settings`。
 * PATCH 只带改动的字段，null = 删除覆盖、回落 orynd.yaml；响应就是新的完整状态，直接写回缓存。
 * 保留期会反映在统计的 `meta.retention` 里，所以写入成功后让它重新拉取。
 */
import { type QueryClient, useQuery } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";
import { statsKeys } from "./stats-queries";

export type OpsSettings = Schemas["OpsSettings"];
export type OpsSettingsPatch = Schemas["OpsSettingsPatch"];
export type OpsSettingsState = Schemas["OpsSettingsState"];

export const opsKeys = {
	state: ["server", "ops-settings"],
} as const;

export function useOpsSettings() {
	return useQuery({
		queryKey: opsKeys.state,
		queryFn: () => unwrap(api.GET("/api/v1/server/ops-settings")),
	});
}

export async function patchOpsSettings(
	qc: QueryClient,
	patch: OpsSettingsPatch,
): Promise<OpsSettingsState> {
	const res = await unwrap(
		api.PATCH("/api/v1/server/ops-settings", { body: patch }),
	);
	qc.setQueryData(opsKeys.state, res);
	void qc.invalidateQueries({ queryKey: statsKeys.meta });
	return res;
}
