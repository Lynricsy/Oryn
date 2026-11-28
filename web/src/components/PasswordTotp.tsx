/**
 * 密码 + TOTP 登录的共用表单件：6 位验证码输入，以及登记 TOTP（二维码 + 手动输入用的密钥 + 新密码两次 + 验证码）。
 * 初始化页（/setup）与设置 → 账户里的「设置 / 重设密码登录」共用登记部分；登录页与重新验证共用验证码输入。
 * TOTP 规格由服务端定（SHA1、6 位、30 秒），`otpauthUri` 已带全部参数，这里只负责渲染。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../api/client";
import { CopyButton } from "./ui/CopyButton";
import { Field, TextInput } from "./ui/primitives";
import { QrCode } from "./ui/QrCode";

const PASSWORD_MIN = 8;
const PASSWORD_MAX = 256;

export const isTotpCode = (s: string): boolean => /^\d{6}$/.test(s);

/** 验证码输入：只留数字（粘贴的「123 456」也能用），最多 6 位。 */
export function TotpCodeInput({
	value,
	onChange,
	autoFocus,
}: {
	value: string;
	onChange: (code: string) => void;
	autoFocus?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Field label={t("auth.totpCode")}>
			<TextInput
				value={value}
				autoFocus={autoFocus}
				inputMode="numeric"
				autoComplete="one-time-code"
				placeholder="000000"
				className="font-mono tabular-nums tracking-widest"
				onChange={(e) =>
					onChange(e.target.value.replace(/\D/g, "").slice(0, 6))
				}
			/>
		</Field>
	);
}

/** 登记表单的状态与前端校验结果。 */
export interface PasswordEnrollForm {
	password: string;
	setPassword: (v: string) => void;
	confirm: string;
	setConfirm: (v: string) => void;
	code: string;
	setCode: (v: string) => void;
	/** 已输入但不足 8 个字符 */
	tooShort: boolean;
	/** 已输入确认密码但与密码不一致 */
	mismatch: boolean;
	/** 密码 8–256 个字符且两次一致、验证码 6 位数字 */
	valid: boolean;
}

export function usePasswordEnrollForm(): PasswordEnrollForm {
	const [password, setPassword] = useState("");
	const [confirm, setConfirm] = useState("");
	const [code, setCode] = useState("");
	const length = [...password].length;
	const tooShort = length > 0 && length < PASSWORD_MIN;
	const mismatch = confirm !== "" && confirm !== password;
	const valid =
		length >= PASSWORD_MIN &&
		length <= PASSWORD_MAX &&
		confirm === password &&
		isTotpCode(code);
	return {
		password,
		setPassword,
		confirm,
		setConfirm,
		code,
		setCode,
		tooShort,
		mismatch,
		valid,
	};
}

/** 登记 TOTP 并设置密码：先在验证器 App 里扫码（或手动输入密钥），再填密码与 App 上显示的验证码。 */
export function PasswordEnrollFields({
	enrollment,
	form,
}: {
	enrollment: Schemas["TotpEnrollment"];
	form: PasswordEnrollForm;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div className="flex flex-col gap-3">
			<p className="text-muted text-sm">{t("auth.totpScanHint")}</p>
			<div className="flex flex-wrap items-start gap-4">
				<QrCode
					text={enrollment.otpauthUri}
					label={t("auth.totpQrLabel")}
					className="h-40 w-40"
				/>
				<div className="flex min-w-40 flex-1 flex-col items-start gap-1.5">
					<span className="text-muted text-xs">{t("auth.totpManual")}</span>
					<code className="break-all font-mono text-[12.5px] text-fg">
						{enrollment.secret}
					</code>
					<CopyButton text={enrollment.secret} label={t("common.copy")} />
				</div>
			</div>
			<Field
				label={t("auth.newPassword")}
				hint={
					form.tooShort ? (
						<span className="text-err">{t("auth.passwordTooShort")}</span>
					) : (
						t("auth.passwordRule")
					)
				}
			>
				<TextInput
					type="password"
					autoComplete="new-password"
					maxLength={PASSWORD_MAX}
					value={form.password}
					onChange={(e) => form.setPassword(e.target.value)}
				/>
			</Field>
			<Field
				label={t("auth.confirmPassword")}
				hint={
					form.mismatch ? (
						<span className="text-err">{t("auth.passwordMismatch")}</span>
					) : undefined
				}
			>
				<TextInput
					type="password"
					autoComplete="new-password"
					maxLength={PASSWORD_MAX}
					value={form.confirm}
					onChange={(e) => form.setConfirm(e.target.value)}
				/>
			</Field>
			<TotpCodeInput value={form.code} onChange={form.setCode} />
		</div>
	);
}
