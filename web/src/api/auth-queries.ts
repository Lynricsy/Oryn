/**
 * 设置 → 账户与设备里的 Passkey 管理与恢复码：列表、恢复码状态的查询，以及重新验证、增删改、重新生成。
 * WebAuthn 仪式与登录页一致：begin 返回 `{ceremonyId, options}`，交给浏览器后把凭据回传 finish。
 * 系统的 Passkey 提示必须由用户的一次点击直接触发（Safari 缺少用户手势会拒绝），所以这里的函数只在点击回调里调。
 */
import {
	type PublicKeyCredentialCreationOptionsJSON,
	type PublicKeyCredentialRequestOptionsJSON,
	startAuthentication,
	startRegistration,
} from "@simplewebauthn/browser";
import { type QueryClient, useQuery } from "@tanstack/react-query";
import { unwrapOptions } from "../lib/webauthn";
import { api, type Schemas, unwrap } from "./client";
import { qk } from "./queries";

export const passkeyKeys = {
	list: ["auth", "passkeys"],
	recovery: ["auth", "recovery-codes"],
} as const;

export function usePasskeys() {
	return useQuery({
		queryKey: passkeyKeys.list,
		queryFn: () => unwrap(api.GET("/api/v1/auth/passkeys")),
	});
}

export function useRecoveryCodeStatus() {
	return useQuery({
		queryKey: passkeyKeys.recovery,
		queryFn: () => unwrap(api.GET("/api/v1/auth/recovery-codes")),
	});
}

/** 对当前会话做一次 Passkey 验证（5 分钟内有效），之后列表里的 `reauthValidUntil` 随之刷新。 */
export async function reauth(
	qc: QueryClient,
): Promise<Schemas["ReauthResult"]> {
	const begin = await unwrap(api.POST("/api/v1/auth/reauth/begin"));
	const credential = await startAuthentication({
		optionsJSON: unwrapOptions<PublicKeyCredentialRequestOptionsJSON>(
			begin.options,
		),
	});
	const res = await unwrap(
		api.POST("/api/v1/auth/reauth/finish", {
			body: {
				ceremonyId: begin.ceremonyId,
				credential: credential as unknown as Record<string, unknown>,
			},
		}),
	);
	await qc.invalidateQueries({ queryKey: passkeyKeys.list });
	return res;
}

/** 追加一把 Passkey（需要已在 5 分钟内验证过）。 */
export async function addPasskey(
	qc: QueryClient,
	name: string,
): Promise<Schemas["Passkey"]> {
	const begin = await unwrap(
		api.POST("/api/v1/auth/passkeys/begin", { body: { name } }),
	);
	const credential = await startRegistration({
		optionsJSON: unwrapOptions<PublicKeyCredentialCreationOptionsJSON>(
			begin.options,
		),
	});
	const res = await unwrap(
		api.POST("/api/v1/auth/passkeys/finish", {
			body: {
				ceremonyId: begin.ceremonyId,
				credential: credential as unknown as Record<string, unknown>,
			},
		}),
	);
	await Promise.all([
		qc.invalidateQueries({ queryKey: passkeyKeys.list }),
		qc.invalidateQueries({ queryKey: qk.me }),
	]);
	return res;
}

export async function renamePasskey(
	qc: QueryClient,
	id: string,
	name: string,
): Promise<Schemas["Passkey"]> {
	const res = await unwrap(
		api.PATCH("/api/v1/auth/passkeys/{passkeyId}", {
			params: { path: { passkeyId: id } },
			body: { name },
		}),
	);
	await qc.invalidateQueries({ queryKey: passkeyKeys.list });
	return res;
}

/** 删除一把 Passkey（需要已在 5 分钟内验证过）；用它登录的其它会话由服务端立即吊销。 */
export async function deletePasskey(
	qc: QueryClient,
	id: string,
): Promise<void> {
	await unwrap(
		api.DELETE("/api/v1/auth/passkeys/{passkeyId}", {
			params: { path: { passkeyId: id } },
		}),
	);
	await Promise.all([
		qc.invalidateQueries({ queryKey: passkeyKeys.list }),
		qc.invalidateQueries({ queryKey: qk.me }),
	]);
}

/** 作废全部旧码并生成新码（需要已在 5 分钟内验证过）；明文只在返回值里出现这一次。 */
export async function regenerateRecoveryCodes(
	qc: QueryClient,
): Promise<string[]> {
	const res = await unwrap(api.POST("/api/v1/auth/recovery-codes"));
	await Promise.all([
		qc.invalidateQueries({ queryKey: passkeyKeys.recovery }),
		qc.invalidateQueries({ queryKey: qk.me }),
	]);
	return res.recoveryCodes;
}
