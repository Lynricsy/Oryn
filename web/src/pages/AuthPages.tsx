/**
 * 认证页：`/setup`（首次初始化 / 恢复后重新设置登录方式，展示 10 个恢复码）、`/login`（可发现凭据登录、密码 + TOTP、恢复码）、
 * `/pair`（批准 CLI / 原生 App 的设备配对码）。WebAuthn 仪式：begin 返回 `{ceremonyId, options}`，finish 回传。
 * 密码 + TOTP 是 Passkey 之外的另一种登录方式：浏览器不支持 WebAuthn 时直接展示它。
 */
import {
	browserSupportsWebAuthn,
	type PublicKeyCredentialCreationOptionsJSON,
	type PublicKeyCredentialRequestOptionsJSON,
	startAuthentication,
	startRegistration,
} from "@simplewebauthn/browser";
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useSearchParams } from "react-router";
import { api, type Schemas, unwrap } from "../api/client";
import { qk, useMe } from "../api/queries";
import { AuthCard } from "../components/AuthCard";
import { PairApproval } from "../components/PairApproval";
import {
	isTotpCode,
	PasswordEnrollFields,
	TotpCodeInput,
	usePasswordEnrollForm,
} from "../components/PasswordTotp";
import { CopyButton } from "../components/ui/CopyButton";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextInput,
} from "../components/ui/primitives";
import { unwrapOptions } from "../lib/webauthn";

/** 初始化令牌放在 URL 片段（`/setup#token=ost_…`），不进反代访问日志。 */
export function setupTokenFromHash(hash: string): string | undefined {
	return new URLSearchParams(hash.replace(/^#/, "")).get("token") || undefined;
}

/** 次要入口（改用另一种方式）的文字按钮 */
function SwitchLink({
	onClick,
	children,
}: {
	onClick: () => void;
	children: ReactNode;
}) {
	return (
		<button
			type="button"
			className="text-muted text-xs hover:text-fg"
			onClick={onClick}
		>
			{children}
		</button>
	);
}

export function SetupPage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const qc = useQueryClient();
	const [token] = useState(() => setupTokenFromHash(window.location.hash));
	// 读到令牌后从地址栏抹掉片段（不留在历史记录与截图里）
	useEffect(() => {
		if (!setupTokenFromHash(window.location.hash)) return;
		window.history.replaceState(
			window.history.state,
			"",
			window.location.pathname + window.location.search,
		);
	}, []);
	const [webauthn] = useState(() => browserSupportsWebAuthn());
	const [method, setMethod] = useState<"passkey" | "password">(() =>
		webauthn ? "passkey" : "password",
	);
	const [name, setName] = useState(() => navigator.platform || "Passkey");
	const [enrollment, setEnrollment] = useState<
		Schemas["TotpEnrollment"] | null
	>(null);
	const form = usePasswordEnrollForm();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [codes, setCodes] = useState<string[] | null>(null);

	const attempt = async (fn: () => Promise<void>) => {
		setBusy(true);
		setError(null);
		try {
			await fn();
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	/** 两种方式完成后相同：首次初始化展示恢复码，恢复流程回首页。 */
	const finished = async (res: Schemas["AuthSetupFinishResponse"]) => {
		await qc.invalidateQueries({ queryKey: qk.me });
		if (res.recoveryCodes && res.recoveryCodes.length > 0)
			setCodes(res.recoveryCodes);
		else navigate("/");
	};

	const register = () =>
		attempt(async () => {
			const begin = await unwrap(
				api.POST("/api/v1/auth/setup/begin", {
					body: { ...(token ? { token } : {}), name },
				}),
			);
			const credential = await startRegistration({
				optionsJSON: unwrapOptions<PublicKeyCredentialCreationOptionsJSON>(
					begin.options,
				),
			});
			await finished(
				await unwrap(
					api.POST("/api/v1/auth/setup/finish", {
						body: {
							ceremonyId: begin.ceremonyId,
							credential: credential as unknown as Record<string, unknown>,
						},
					}),
				),
			);
		});

	const beginPassword = () =>
		attempt(async () => {
			setMethod("password");
			setEnrollment(
				await unwrap(
					api.POST("/api/v1/auth/password/setup/begin", {
						body: token ? { token } : {},
					}),
				),
			);
		});

	const finishPassword = () => {
		if (!enrollment || !form.valid) return;
		void attempt(async () => {
			await finished(
				await unwrap(
					api.POST("/api/v1/auth/password/setup/finish", {
						body: {
							enrollmentId: enrollment.enrollmentId,
							password: form.password,
							code: form.code,
						},
					}),
				),
			);
		});
	};

	if (codes) {
		return (
			<AuthCard title={t("auth.recoveryCodesTitle")}>
				<p className="mb-3 text-muted text-sm">{t("auth.recoveryCodesHint")}</p>
				<ul className="mb-3 grid grid-cols-2 gap-1.5 whitespace-nowrap font-mono text-xs">
					{codes.map((c) => (
						<li
							key={c}
							className="rounded-md bg-inset px-2 py-1 text-center tabular-nums"
						>
							{c}
						</li>
					))}
				</ul>
				<div className="flex items-center justify-between">
					<CopyButton text={codes.join("\n")} label={t("common.copy")} />
					<Button variant="primary" onClick={() => navigate("/")}>
						{t("auth.savedContinue")}
					</Button>
				</div>
			</AuthCard>
		);
	}

	if (method === "password") {
		return (
			<AuthCard
				title={token ? t("auth.setupTitle") : t("auth.rePasswordTitle")}
			>
				<p className="mb-4 text-muted text-sm">
					{token ? t("auth.passwordSetupHint") : t("auth.rePasswordHint")}
				</p>
				<div className="flex flex-col gap-3">
					{enrollment ? (
						<form
							className="flex flex-col gap-3"
							onSubmit={(e) => {
								e.preventDefault();
								finishPassword();
							}}
						>
							<PasswordEnrollFields enrollment={enrollment} form={form} />
							<Button
								type="submit"
								variant="primary"
								disabled={busy || !form.valid}
							>
								{busy ? <Spinner /> : t("auth.finishSetup")}
							</Button>
						</form>
					) : (
						<Button
							variant="primary"
							disabled={busy}
							onClick={() => void beginPassword()}
						>
							{busy ? <Spinner /> : t("auth.startPasswordSetup")}
						</Button>
					)}
					{webauthn && (
						<SwitchLink
							onClick={() => {
								setMethod("passkey");
								setEnrollment(null);
								setError(null);
							}}
						>
							{t("auth.usePasskeyInstead")}
						</SwitchLink>
					)}
					<ErrorText error={error} />
				</div>
			</AuthCard>
		);
	}

	return (
		<AuthCard title={token ? t("auth.setupTitle") : t("auth.reRegisterTitle")}>
			<p className="mb-4 text-muted text-sm">
				{token ? t("auth.setupHint") : t("auth.reRegisterHint")}
			</p>
			<div className="flex flex-col gap-3">
				<Field label={t("auth.passkeyName")}>
					<TextInput value={name} onChange={(e) => setName(e.target.value)} />
				</Field>
				<Button
					variant="primary"
					disabled={busy}
					onClick={() => void register()}
				>
					{busy ? <Spinner /> : t("auth.createPasskey")}
				</Button>
				<SwitchLink onClick={() => void beginPassword()}>
					{t("auth.usePasswordInstead")}
				</SwitchLink>
				<ErrorText error={error} />
			</div>
		</AuthCard>
	);
}

/** 整页跳转（测试里替换）：服务端端点不在 SPA 路由里，不能走 `navigate`。 */
export const fullPage = {
	replace(url: string): void {
		window.location.replace(url);
	},
};

/**
 * 登录成功后的去处：`/portal/auth?…`（portal 握手的主域名端点，计划 M4 4.3）整页跳转到服务端，
 * 由它签发一次性码并回跳 portal 主机；其余站内路径走路由内跳转，非站内路径回首页。
 */
export function goAfterLogin(
	next: string,
	navigate: (to: string, opts: { replace: boolean }) => void,
): void {
	if (next.startsWith("/portal/auth?")) fullPage.replace(next);
	else navigate(next.startsWith("/") ? next : "/", { replace: true });
}

export function LoginPage(): ReactNode {
	const { t } = useTranslation();
	const [params] = useSearchParams();
	const navigate = useNavigate();
	const qc = useQueryClient();
	const next = params.get("next") || "/";
	const [webauthn] = useState(() => browserSupportsWebAuthn());
	const [pending, setPending] = useState<
		"passkey" | "password" | "recovery" | null
	>(null);
	const [error, setError] = useState<unknown>(null);
	const [passwordOpen, setPasswordOpen] = useState(!webauthn);
	const [password, setPassword] = useState("");
	const [code, setCode] = useState("");
	const [recovery, setRecovery] = useState<string | null>(null);
	const busy = pending !== null;

	const attempt = async (
		kind: "passkey" | "password" | "recovery",
		fn: () => Promise<void>,
	) => {
		setPending(kind);
		setError(null);
		try {
			await fn();
		} catch (e) {
			setError(e);
		} finally {
			setPending(null);
		}
	};

	const login = () =>
		attempt("passkey", async () => {
			const begin = await unwrap(api.POST("/api/v1/auth/login/begin"));
			const credential = await startAuthentication({
				optionsJSON: unwrapOptions<PublicKeyCredentialRequestOptionsJSON>(
					begin.options,
				),
			});
			await unwrap(
				api.POST("/api/v1/auth/login/finish", {
					body: {
						ceremonyId: begin.ceremonyId,
						credential: credential as unknown as Record<string, unknown>,
					},
				}),
			);
			await qc.invalidateQueries({ queryKey: qk.me });
			goAfterLogin(next, navigate);
		});

	const passwordValid = password !== "" && isTotpCode(code);
	const submitPassword = () => {
		if (!passwordValid) return;
		void attempt("password", async () => {
			try {
				await unwrap(
					api.POST("/api/v1/auth/password/login", {
						body: { password, code },
					}),
				);
			} catch (e) {
				// 同一个验证码只能用一次：失败后清空，等验证器 App 上的下一个
				setCode("");
				throw e;
			}
			await qc.invalidateQueries({ queryKey: qk.me });
			goAfterLogin(next, navigate);
		});
	};

	const submitRecovery = () => {
		if (!recovery?.trim()) return;
		void attempt("recovery", async () => {
			await unwrap(
				api.POST("/api/v1/auth/recovery", { body: { code: recovery.trim() } }),
			);
			navigate("/setup");
		});
	};

	const passkeyButton = webauthn && (
		<Button
			variant={passwordOpen ? "secondary" : "primary"}
			disabled={busy}
			onClick={() => void login()}
		>
			{pending === "passkey" ? <Spinner /> : t("auth.loginWithPasskey")}
		</Button>
	);

	return (
		<AuthCard title={t("auth.loginTitle")} blink>
			<div className="flex flex-col gap-3">
				{passwordOpen ? (
					<>
						<form
							className="flex flex-col gap-3"
							onSubmit={(e) => {
								e.preventDefault();
								submitPassword();
							}}
						>
							<Field label={t("auth.password")}>
								<TextInput
									type="password"
									autoFocus
									autoComplete="current-password"
									value={password}
									onChange={(e) => setPassword(e.target.value)}
								/>
							</Field>
							<TotpCodeInput value={code} onChange={setCode} />
							<Button
								type="submit"
								variant="primary"
								disabled={busy || !passwordValid}
							>
								{pending === "password" ? (
									<Spinner />
								) : (
									t("auth.loginWithPassword")
								)}
							</Button>
						</form>
						{passkeyButton}
					</>
				) : (
					<>
						{passkeyButton}
						<SwitchLink onClick={() => setPasswordOpen(true)}>
							{t("auth.usePassword")}
						</SwitchLink>
					</>
				)}
				{recovery === null ? (
					<SwitchLink onClick={() => setRecovery("")}>
						{t("auth.useRecoveryCode")}
					</SwitchLink>
				) : (
					<form
						className="flex flex-col gap-2"
						onSubmit={(e) => {
							e.preventDefault();
							submitRecovery();
						}}
					>
						<Field label={t("auth.recoveryCode")}>
							<TextInput
								autoFocus
								value={recovery}
								onChange={(e) => setRecovery(e.target.value)}
							/>
						</Field>
						<Button type="submit" disabled={busy || !recovery.trim()}>
							{pending === "recovery" ? <Spinner /> : t("auth.recover")}
						</Button>
					</form>
				)}
				<ErrorText error={error} />
			</div>
		</AuthCard>
	);
}

export function PairPage(): ReactNode {
	const { t } = useTranslation();
	const me = useMe();
	if (me.isPending) {
		return (
			<AuthCard title={t("pair.title")} blink>
				<Spinner />
			</AuthCard>
		);
	}
	if (me.isError) {
		return (
			<AuthCard title={t("pair.title")} blink>
				<p className="mb-3 text-muted text-sm">{t("pair.loginFirst")}</p>
				<Link
					className="text-accent text-sm underline underline-offset-3"
					to={`/login?next=${encodeURIComponent("/pair")}`}
				>
					{t("auth.loginTitle")}
				</Link>
			</AuthCard>
		);
	}
	return (
		<AuthCard title={t("pair.title")} blink>
			<p className="mb-3 text-muted text-sm">{t("pair.hint")}</p>
			<PairApproval />
		</AuthCard>
	);
}
