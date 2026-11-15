/**
 * 分享页的身份（M9.6 §6 / §7.4，M13 R9）：匿名接口 `GET /api/v1/public/shares/<slug>/identity` 给出全局 agent 身份、
 * 这段对话里用到且设了头像的 role（键是公开条目里来源记录的不透明键），以及 task 工具卡片里用到且设了头像的
 * 子 agent（按名字明文对应：agent 名本就公开显示在卡片里）。不碰任何 `/api/v1/settings/`。
 * 快照分享的身份在创建时固化，读一次即可；实时分享跟随当前设置，由页面在重连与出现新键 / 新 agent 时失效重取。
 */
import { useQuery } from "@tanstack/react-query";
import { ApiError, api, type Schemas, unwrap } from "../api/client";
import type { AgentFace } from "../api/identity-queries";
import { agentDefaultGlyph } from "../lib/avatar-defaults";

export type PublicShareIdentity = Schemas["PublicShareIdentity"];
export type PublicAgentIdentity = Schemas["PublicAgentIdentity"];

export const shareIdentityKey = (slug: string) =>
	["share", slug, "identity"] as const;

/** 未找到（撤销、升级前的环境）一律当作没有身份：页面显示默认品牌标与各 agent 的默认字形。 */
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
		if (e instanceof ApiError && e.status === 404)
			return { roles: [], agents: [] };
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

/**
 * 分享页里一个子 agent 的头像：公开身份里有（设了自定义头像）就用它，否则用该 agent 的默认字形。
 * 与站内的 `agentFace` 同一判定，只是来源换成公开身份；名字区分大小写。
 */
export function shareAgentFace(
	agents: readonly PublicAgentIdentity[] | undefined,
	name: string,
): AgentFace {
	const glyph = agentDefaultGlyph(name);
	const own = agents?.find((a) => a.name === name);
	const face: Schemas["AvatarIdentity"] = {};
	if (own?.avatarIcon) face.avatarIcon = own.avatarIcon;
	if (own?.avatarImage) face.avatarImage = own.avatarImage;
	return face.avatarIcon || face.avatarImage ? { face, glyph } : { glyph };
}
