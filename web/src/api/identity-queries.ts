/**
 * 身份设置（M9.6）的查询与写入：全局 agent 身份、全局助手身份、用户头像、role 头像。
 *
 * 新鲜期 5 分钟：转录里每个回合头都挂头像，虚拟列表滚动时反复挂载，默认的 0 会让每次挂载都重新请求。
 * 写入方（设置页）成功后直接写回缓存，不靠重新拉取；写入前后都取消在途的读取，
 * 否则 PUT 之前发出的旧 GET 后到会把缓存还原成旧值。queryFn 故意不接 `signal`：
 * 接了之后最后一个观察者卸载（滚出视口）就会中止在途请求，下次挂载又重发。
 */
import {
	type QueryClient,
	type UseQueryResult,
	useQuery,
} from "@tanstack/react-query";
import { useMemo } from "react";
import { api, type Schemas, unwrap } from "./client";

export type IdentityOwner = "agent" | "assistant" | "user";

export const identityKeys = {
	agent: ["settings", "agent-identity"],
	assistant: ["settings", "assistant-identity"],
	user: ["settings", "user-identity"],
	roles: ["settings", "role-identities"],
} as const;

const SETTINGS_STALE_MS = 5 * 60_000;

type AgentIdentity = Schemas["AgentIdentity"];
type AvatarIdentity = Schemas["AvatarIdentity"];
type RoleIdentityList = Schemas["RoleIdentityList"];

/** 只取头像两个字段（空串视为未设置）。 */
function avatarOf(v: AvatarIdentity | undefined): AvatarIdentity {
	const out: AvatarIdentity = {};
	if (v?.avatarIcon) out.avatarIcon = v.avatarIcon;
	if (v?.avatarImage) out.avatarImage = v.avatarImage;
	return out;
}

function fetchIdentity(who: IdentityOwner): Promise<AgentIdentity> {
	switch (who) {
		case "agent":
			return unwrap(api.GET("/api/v1/settings/agent-identity"));
		case "assistant":
			return unwrap(api.GET("/api/v1/settings/assistant-identity"));
		case "user":
			return unwrap(api.GET("/api/v1/settings/user-identity"));
	}
}

function putIdentity(
	who: IdentityOwner,
	body: AgentIdentity,
): Promise<AgentIdentity> {
	switch (who) {
		case "agent":
			return unwrap(api.PUT("/api/v1/settings/agent-identity", { body }));
		case "assistant":
			return unwrap(api.PUT("/api/v1/settings/assistant-identity", { body }));
		case "user":
			// 用户身份只有头像；名字字段不发
			return unwrap(
				api.PUT("/api/v1/settings/user-identity", { body: avatarOf(body) }),
			);
	}
}

/** 读某个身份（原始值；助手身份的回落见 `useAssistantIdentity`）。 */
export function useIdentity(
	who: IdentityOwner,
	enabled = true,
): UseQueryResult<AgentIdentity> {
	return useQuery({
		queryKey: identityKeys[who],
		queryFn: () => fetchIdentity(who),
		staleTime: SETTINGS_STALE_MS,
		enabled,
	});
}

/** 整体替换；调用方传完整对象（名字 + 头像），`{}` = 全部清除。 */
export async function saveIdentity(
	qc: QueryClient,
	who: IdentityOwner,
	body: AgentIdentity,
): Promise<void> {
	const queryKey = identityKeys[who];
	await qc.cancelQueries({ queryKey });
	const saved = await putIdentity(who, body);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData(queryKey, saved);
}

export function useRoleIdentities(
	enabled = true,
): UseQueryResult<RoleIdentityList> {
	return useQuery({
		queryKey: identityKeys.roles,
		queryFn: () => unwrap(api.GET("/api/v1/settings/role-identities")),
		staleTime: SETTINGS_STALE_MS,
		enabled,
	});
}

/** 把写入后的 role 换进列表（清除了头像的遗留项从列表里删掉；列表里没有的追加到末尾）。 */
export function mergeRoleIdentity(
	list: RoleIdentityList,
	saved: Schemas["RoleIdentity"],
): RoleIdentityList {
	const gone = !saved.inCycle && !saved.avatarIcon && !saved.avatarImage;
	const at = list.roles.findIndex((r) => r.role === saved.role);
	if (gone)
		return at < 0
			? list
			: { ...list, roles: list.roles.filter((_, i) => i !== at) };
	const roles =
		at < 0
			? [...list.roles, saved]
			: list.roles.map((r, i) => (i === at ? saved : r));
	return { ...list, roles };
}

/** `{}` = 清除；成功后把返回的 RoleIdentity 换进列表缓存（清除遗留项时从列表里删掉）。 */
export async function saveRoleAvatar(
	qc: QueryClient,
	role: string,
	avatar: AvatarIdentity,
): Promise<void> {
	const queryKey = identityKeys.roles;
	await qc.cancelQueries({ queryKey });
	const saved = await unwrap(
		api.PUT("/api/v1/settings/role-identities", {
			body: { role, ...avatarOf(avatar) },
		}),
	);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData<RoleIdentityList>(queryKey, (old) =>
		old ? mergeRoleIdentity(old, saved) : old,
	);
}

export interface EffectiveIdentity {
	name?: string;
	avatar: AvatarIdentity;
}

/** 名字、头像各自缺省时取 agent 的（纯函数）。 */
export function effectiveAssistant(
	assistant: AgentIdentity | undefined,
	agent: AgentIdentity | undefined,
): EffectiveIdentity {
	const own = avatarOf(assistant);
	const avatar = own.avatarIcon || own.avatarImage ? own : avatarOf(agent);
	const name = assistant?.name?.trim() || agent?.name?.trim();
	return name ? { name, avatar } : { avatar };
}

/** 全局助手的生效身份（自己的 → agent 的；都没有则 `{avatar: {}}`）。 */
export function useAssistantIdentity(enabled = true): EffectiveIdentity {
	const assistant = useIdentity("assistant", enabled).data;
	const agent = useIdentity("agent", enabled).data;
	return useMemo(
		() => effectiveAssistant(assistant, agent),
		[assistant, agent],
	);
}
