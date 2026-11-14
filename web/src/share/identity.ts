/**
 * 分享页的身份（M9.6 §6 / §7.4）：匿名接口 `GET /api/v1/public/shares/<slug>/identity` 给出全局 agent 身份与
 * 这段对话里用到、且设了头像的 role（键是公开条目里来源记录的不透明键）。不碰任何 `/api/v1/settings/`。
 * 快照分享的身份在创建时固化，读一次即可；实时分享跟随当前设置，由页面在重连与出现新键时失效重取。
 */
import { useQuery } from "@tanstack/react-query";
import { ApiError, api, type Schemas, unwrap } from "../api/client";

export type PublicShareIdentity = Schemas["PublicShareIdentity"];

export const shareIdentityKey = (slug: string) =>
	["share", slug, "identity"] as const;

/** 未找到（撤销、升级前的环境）一律当作没有身份：页面显示默认品牌标。 */
export async function fetchShareIdentity(
	slug: string,
): Promise<PublicShareIdentity> {
	try {
		return await unwrap(
			api.GET("/api/v1/public/shares/{slug}/identity", {
				params: { path: { slug } },
			}),
		);
	} catch (e) {
		if (e instanceof ApiError && e.status === 404) return { roles: [] };
		throw e;
	}
}

/** 快照与实时都不按时间过期：快照本就固化；实时由页面在重连与出现新键时显式失效。 */
export function useShareIdentity(slug: string) {
	return useQuery({
		queryKey: shareIdentityKey(slug),
		queryFn: () => fetchShareIdentity(slug),
		staleTime: Number.POSITIVE_INFINITY,
	});
}
