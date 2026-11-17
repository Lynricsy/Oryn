/**
 * SSH 主机与网关（M18）的查询与写入：主机（全局与项目）、钥匙、测试与快照、重置指纹、网关审计、线程可用的主机。
 * 变更成功后让 `sshKeys.all` 失效；别处（另一台设备、命令行、网关固定指纹）的变化经 WS `ssh.changed` 推送，
 * `useRealtimeSshSync` 据此（以及每次 WS 重新连上时）让 SSH 查询全部失效。
 */
import {
	useInfiniteQuery,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { useEffect } from "react";
import { type RealtimeClient, realtime } from "../realtime/socket";
import { api, type Schemas, unwrap } from "./client";

export type SshHost = Schemas["SshHost"];
export type SshHostCreate = Schemas["SshHostCreate"];
export type SshHostUpdate = Schemas["SshHostUpdate"];
export type SshTestResult = Schemas["SshTestResult"];
export type SshSnapshot = Schemas["SshSnapshot"];
export type SshKey = Schemas["SshKey"];
export type SshKeyCreate = Schemas["SshKeyCreate"];
export type SshAuditEntry = Schemas["SshAuditEntry"];
export type ThreadSshHosts = Schemas["ThreadSshHosts"];
export type ThreadSshHost = Schemas["ThreadSshHost"];

/** 审计列表的过滤条件（都可省略）。 */
export interface SshAuditFilter {
	/** 主机 id */
	host?: string;
	thread?: string;
}

export const sshKeys = {
	all: ["ssh"] as const,
	hosts: ["ssh", "hosts"] as const,
	keys: ["ssh", "keys"] as const,
	audit: (f: SshAuditFilter) =>
		["ssh", "audit", f.host ?? "", f.thread ?? ""] as const,
	thread: (id: string) => ["ssh", "thread", id] as const,
};

/**
 * SSH 查询与服务端对账：收到 `ssh.changed`、或 WS（重新）连上时，`["ssh"]` 前缀的查询全部失效。
 * 在调 `realtime.connect()` 的应用壳里调用一次。
 */
export function useRealtimeSshSync(
	client: Pick<RealtimeClient, "onSshChanged"> = realtime,
): void {
	const qc = useQueryClient();
	useEffect(
		() =>
			client.onSshChanged(() => {
				void qc.invalidateQueries({ queryKey: sshKeys.all });
			}),
		[client, qc],
	);
}

/** 全部主机（全局在前，再按项目、名称）。 */
export function useSshHosts(enabled = true) {
	return useQuery({
		queryKey: sshKeys.hosts,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/ssh/hosts")),
		staleTime: 30_000,
	});
}

export function useSshKeys(enabled = true) {
	return useQuery({
		queryKey: sshKeys.keys,
		enabled,
		queryFn: () => unwrap(api.GET("/api/v1/ssh/keys")),
		staleTime: 30_000,
	});
}

/** 每页的审计条数（服务端上限 200）。 */
export const SSH_AUDIT_PAGE = 50;

/** 网关审计（按时间倒序）；下一页用上一页的 `nextBefore` 作 `before`。 */
export function useSshAudit(filter: SshAuditFilter, enabled = true) {
	return useInfiniteQuery({
		queryKey: sshKeys.audit(filter),
		enabled,
		initialPageParam: undefined as number | undefined,
		queryFn: ({ pageParam }) =>
			unwrap(
				api.GET("/api/v1/ssh/audit", {
					params: {
						query: {
							limit: SSH_AUDIT_PAGE,
							...(filter.host ? { host: filter.host } : {}),
							...(filter.thread ? { thread: filter.thread } : {}),
							...(pageParam !== undefined ? { before: pageParam } : {}),
						},
					},
				}),
			),
		getNextPageParam: (last) => last.nextBefore,
		staleTime: 30_000,
	});
}

/** 线程所在 orb 能用的主机；runner / 协调线程 `managed=false`。 */
export function useThreadSshHosts(threadId: string, enabled = true) {
	return useQuery({
		queryKey: sshKeys.thread(threadId),
		enabled,
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/threads/{id}/ssh-hosts", {
					params: { path: { id: threadId } },
				}),
			),
		staleTime: 30_000,
	});
}

function useInvalidateSsh() {
	const qc = useQueryClient();
	return () => qc.invalidateQueries({ queryKey: sshKeys.all });
}

export function useCreateSshHost() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: (body: SshHostCreate) =>
			unwrap(api.POST("/api/v1/ssh/hosts", { body })),
		onSuccess: () => invalidate(),
	});
}

export function useUpdateSshHost() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: ({ id, patch }: { id: string; patch: SshHostUpdate }) =>
			unwrap(
				api.PATCH("/api/v1/ssh/hosts/{id}", {
					params: { path: { id } },
					body: patch,
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteSshHost() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.DELETE("/api/v1/ssh/hosts/{id}", { params: { path: { id } } }),
			),
		onSuccess: () => invalidate(),
	});
}

/** 测试连接并采一次状态快照；连不上也返回 200（`ok=false`）。 */
export function useTestSshHost() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/ssh/hosts/{id}/test", { params: { path: { id } } }),
			),
		onSuccess: () => invalidate(),
	});
}

export function useResetSshHostKey() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(
				api.POST("/api/v1/ssh/hosts/{id}/reset-host-key", {
					params: { path: { id } },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useCreateSshKey() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: (body: SshKeyCreate) =>
			unwrap(api.POST("/api/v1/ssh/keys", { body })),
		onSuccess: () => invalidate(),
	});
}

export function useRenameSshKey() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: ({ id, name }: { id: string; name: string }) =>
			unwrap(
				api.PATCH("/api/v1/ssh/keys/{id}", {
					params: { path: { id } },
					body: { name },
				}),
			),
		onSuccess: () => invalidate(),
	});
}

export function useDeleteSshKey() {
	const invalidate = useInvalidateSsh();
	return useMutation({
		mutationFn: (id: string) =>
			unwrap(api.DELETE("/api/v1/ssh/keys/{id}", { params: { path: { id } } })),
		onSuccess: () => invalidate(),
	});
}

/** 主机的 `用户@地址:端口`。 */
export function sshTarget(h: Pick<SshHost, "username" | "address" | "port">) {
	return `${h.username}@${h.address}:${h.port}`;
}
