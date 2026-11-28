/**
 * mock 的 Passkey 管理、密码登录与恢复码（与控制面同形）：内存里两把 Passkey、密码登录开关与一批恢复码的计数；列表、改名、
 * 删除（只剩一把且没启用密码登录 → 409 `last_passkey`）、设置 / 关闭密码登录（没有 Passkey 时关闭 → 409 `last_login_method`）、
 * 恢复码状态与重新生成（返回 10 个假码）。
 * mock 下没有真实凭据，触发不了系统的 Passkey 提示：`reauthValidUntil` 恒为当前时间 + 5 分钟（页面不会再要求验证），
 * 新增与重新验证的 begin / finish 回 501；密码 + TOTP 不校验，任意 6 位验证码都通过。
 */
import type { Schemas } from "../api/client";
import { NOW } from "./fixtures";

type Passkey = Schemas["Passkey"];

const DAY = 86_400_000;
const REAUTH_MS = 5 * 60_000;

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

const err = (code: string, message: string, status: number) =>
	json({ code, message }, status);

export class MockPasskeys {
	private passkeys: Passkey[] = [
		{
			id: "bW9jay1tYWNib29r",
			name: "MacBook Pro",
			createdAt: NOW - 90 * DAY,
			lastUsedAt: NOW - 2 * 3_600_000,
			transports: ["internal", "hybrid"],
			backupEligible: true,
			backedUp: true,
			current: true,
		},
		{
			id: "bW9jay15dWJpa2V5",
			name: "YubiKey 5C",
			createdAt: NOW - 40 * DAY,
			lastUsedAt: NOW - 12 * DAY,
			transports: ["usb", "nfc"],
			backupEligible: false,
			backedUp: false,
			current: false,
		},
	];
	private recovery = { total: 10, remaining: 8, generatedAt: NOW - 90 * DAY };
	private passwordEnabled = false;

	async handle(req: Request, p: string, m: string): Promise<Response | null> {
		if (p === "auth/passkeys" && m === "GET")
			return json({
				items: this.passkeys,
				reauthValidUntil: Date.now() + REAUTH_MS,
			} satisfies Schemas["PasskeyList"]);
		if (
			p === "auth/passkeys/begin" ||
			p === "auth/passkeys/finish" ||
			p === "auth/reauth/begin" ||
			p === "auth/reauth/finish"
		)
			return err("not_implemented", "mock 下没有真实的 Passkey", 501);
		if (p === "auth/password/reauth" && m === "POST") {
			const verifiedAt = Date.now();
			return json({
				verifiedAt,
				validUntil: verifiedAt + REAUTH_MS,
			} satisfies Schemas["ReauthResult"]);
		}
		if (p === "auth/password/begin" && m === "POST") {
			const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";
			return json({
				enrollmentId: `enr-${Date.now()}`,
				secret,
				otpauthUri: `otpauth://totp/Oryn:owner?secret=${secret}&issuer=Oryn&algorithm=SHA1&digits=6&period=30`,
			} satisfies Schemas["TotpEnrollment"]);
		}
		if (p === "auth/password/finish" && m === "POST") {
			this.passwordEnabled = true;
			return new Response(null, { status: 204 });
		}
		if (p === "auth/password" && m === "DELETE") {
			if (this.passkeys.length === 0)
				return err("last_login_method", "至少保留一种登录方式", 409);
			this.passwordEnabled = false;
			return new Response(null, { status: 204 });
		}
		const one = /^auth\/passkeys\/([^/]+)$/.exec(p);
		if (one) {
			const id = decodeURIComponent(one[1] ?? "");
			const pk = this.passkeys.find((x) => x.id === id);
			if (!pk) return err("not_found", "Passkey 不存在", 404);
			if (m === "PATCH") {
				const b: unknown = await req.json().catch(() => null);
				const name =
					b &&
					typeof b === "object" &&
					"name" in b &&
					typeof b.name === "string"
						? b.name.trim()
						: "";
				if (!name || [...name].length > 80)
					return err("invalid_request", "名称须为 1–80 个字符", 400);
				pk.name = name;
				return json(pk);
			}
			if (m === "DELETE") {
				if (this.passkeys.length <= 1 && !this.passwordEnabled)
					return err("last_passkey", "不能删除最后一把 Passkey", 409);
				this.passkeys = this.passkeys.filter((x) => x !== pk);
				return new Response(null, { status: 204 });
			}
			return null;
		}
		if (p === "auth/recovery-codes") {
			if (m === "GET")
				return json(this.recovery satisfies Schemas["RecoveryCodeStatus"]);
			if (m === "POST") {
				const recoveryCodes = Array.from(
					{ length: 10 },
					(_, i) =>
						`MOCK-${String(Date.now() % 10_000).padStart(4, "0")}-${String(i).padStart(2, "0")}`,
				);
				this.recovery = {
					total: 10,
					remaining: 10,
					generatedAt: Date.now(),
				};
				return json({ recoveryCodes } satisfies Schemas["RecoveryCodes"]);
			}
		}
		return null;
	}

	/** `/me` 的 `passkeyCount`、`passwordEnabled` 与 `recoveryCodesRemaining`。 */
	counts(): {
		passkeyCount: number;
		passwordEnabled: boolean;
		recoveryCodesRemaining: number;
	} {
		return {
			passkeyCount: this.passkeys.length,
			passwordEnabled: this.passwordEnabled,
			recoveryCodesRemaining: this.recovery.remaining,
		};
	}
}
