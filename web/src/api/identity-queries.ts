/**
 * 身份设置（M9.6 / M13）的查询与写入：默认 Agent 身份、全局助手身份、用户头像、role 头像、子 agent 头像、
 * 新对话输入框偏好。
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
import { agentDefaultGlyph } from "../lib/avatar-defaults";
import { api, type Schemas, unwrap } from "./client";

export type IdentityOwner = "agent" | "assistant" | "user";

export const identityKeys = {
	agent: ["settings", "agent-identity"],
	assistant: ["settings", "assistant-identity"],
	user: ["settings", "user-identity"],
	roles: ["settings", "role-identities"],
	agents: ["settings", "agent-avatars"],
	composer: ["settings", "composer"],
} as const;

const SETTINGS_STALE_MS = 5 * 60_000;

type AgentIdentity = Schemas["AgentIdentity"];
type AvatarIdentity = Schemas["AvatarIdentity"];
type RoleIdentityList = Schemas["RoleIdentityList"];
type AgentAvatarList = Schemas["AgentAvatarList"];
type ComposerPrefs = Schemas["ComposerPrefs"];

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

/** 全局助手自己的身份（纯函数）：不回落到 Agent 身份；名字、头像留空时由显示处给「全局助手」与 sparkle 标记。 */
export function effectiveAssistant(
	assistant: AgentIdentity | undefined,
): EffectiveIdentity {
	const avatar = avatarOf(assistant);
	const name = assistant?.name?.trim();
	return name ? { name, avatar } : { avatar };
}

/** 全局助手的生效身份（只看助手自己的设置；都没有则 `{avatar: {}}`）。 */
export function useAssistantIdentity(enabled = true): EffectiveIdentity {
	const assistant = useIdentity("assistant", enabled).data;
	return useMemo(() => effectiveAssistant(assistant), [assistant]);
}

/** 子 agent 头像列表（内置 + `agents/*.md` + 遗留项）。 */
export function useAgentAvatars(
	enabled = true,
): UseQueryResult<AgentAvatarList> {
	return useQuery({
		queryKey: identityKeys.agents,
		queryFn: () => unwrap(api.GET("/api/v1/settings/agent-avatars")),
		staleTime: SETTINGS_STALE_MS,
		enabled,
	});
}

/** 把写入后的 agent 换进列表（清除了头像的遗留项从列表里删掉；列表里没有的追加到末尾）。 */
export function mergeAgentAvatar(
	list: AgentAvatarList,
	saved: Schemas["AgentAvatar"],
): AgentAvatarList {
	const gone =
		saved.source === "orphan" && !saved.avatarIcon && !saved.avatarImage;
	const at = list.agents.findIndex((a) => a.name === saved.name);
	if (gone)
		return at < 0
			? list
			: { ...list, agents: list.agents.filter((_, i) => i !== at) };
	const agents =
		at < 0
			? [...list.agents, saved]
			: list.agents.map((a, i) => (i === at ? saved : a));
	return { ...list, agents };
}

/** `{}` = 清除；成功后把返回的 AgentAvatar 换进列表缓存。 */
export async function saveAgentAvatar(
	qc: QueryClient,
	name: string,
	avatar: AvatarIdentity,
): Promise<void> {
	const queryKey = identityKeys.agents;
	await qc.cancelQueries({ queryKey });
	const saved = await unwrap(
		api.PUT("/api/v1/settings/agent-avatars", {
			body: { name, ...avatarOf(avatar) },
		}),
	);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData<AgentAvatarList>(queryKey, (old) =>
		old ? mergeAgentAvatar(old, saved) : old,
	);
}

/** 一个 agent 的头像：自定义头像（`face`，未设置时省略）与未设置时显示的默认字形。 */
export interface AgentFace {
	face?: AvatarIdentity;
	glyph: string;
}

/** agent 的头像解析（纯函数）：自定义头像 → 内置默认字形 → 通用字形。名字区分大小写。 */
export function agentFace(
	list: AgentAvatarList | undefined,
	name: string,
): AgentFace {
	const glyph = agentDefaultGlyph(name);
	const own = avatarOf(list?.agents.find((a) => a.name === name));
	return own.avatarIcon || own.avatarImage ? { face: own, glyph } : { glyph };
}

/** 站内某个 agent 的头像（读 agent 头像列表；`enabled=false` 时只给默认字形，不发请求）。 */
export function useAgentFace(name: string, enabled = true): AgentFace {
	const list = useAgentAvatars(enabled).data;
	return useMemo(() => agentFace(list, name), [list, name]);
}

/** 新对话输入框的偏好（首屏默认对象）。 */
export function useComposerPrefs(
	enabled = true,
): UseQueryResult<ComposerPrefs> {
	return useQuery({
		queryKey: identityKeys.composer,
		queryFn: () => unwrap(api.GET("/api/v1/settings/composer")),
		staleTime: SETTINGS_STALE_MS,
		enabled,
	});
}

/** 整体替换；成功后写回缓存。 */
export async function saveComposerPrefs(
	qc: QueryClient,
	prefs: ComposerPrefs,
): Promise<void> {
	const queryKey = identityKeys.composer;
	await qc.cancelQueries({ queryKey });
	const saved = await unwrap(
		api.PUT("/api/v1/settings/composer", { body: prefs }),
	);
	await qc.cancelQueries({ queryKey });
	qc.setQueryData(queryKey, saved);
}
