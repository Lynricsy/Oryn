/**
 * 认证页：`/setup`（首次初始化 / 恢复后重新注册 Passkey，展示 10 个恢复码）、`/login`（可发现凭据登录、恢复码）、
 * `/pair`（批准 CLI / 原生 App 的设备配对码）。WebAuthn 仪式：begin 返回 `{ceremonyId, options}`，finish 回传。
 */
import {
	type PublicKeyCredentialCreationOptionsJSON,
	type PublicKeyCredentialRequestOptionsJSON,
	startAuthentication,
	startRegistration,
} from "@simplewebauthn/browser";
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useSearchParams } from "react-router";
import { api, unwrap } from "../api/client";
import { qk, useMe } from "../api/queries";
import { PairApproval } from "../components/PairApproval";
import { CopyButton } from "../components/ui/CopyButton";
import { BrandMark } from "../components/ui/data";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextInput,
} from "../components/ui/primitives";

/**
 * begin 返回的是 `publicKey` 内层 JSON（WebAuthn L3 JSON 形态，由 @simplewebauthn/browser 解码，
 * 等价于 `PublicKeyCredential.parseCreationOptionsFromJSON / parseRequestOptionsFromJSON`）；
 * 若服务端仍包了一层 `{publicKey: …}` 也兼容。
 */
function unwrapOptions<T>(options: Record<string, unknown>): T {
	const inner = options.publicKey;
	return (inner && typeof inner === "object" ? inner : options) as T;
}

function AuthCard({ title, children }: { title: string; children: ReactNode }) {
	const { t } = useTranslation();
	return (
		<div className="shell-wash flex min-h-dvh items-center justify-center px-4 pt-safe pb-safe">
			<div className="glass pop-in w-full max-w-sm rounded-2xl p-6">
				<div className="mb-4 flex items-center gap-2">
					<BrandMark size={32} />
					<span className="font-semibold text-muted text-sm">
						{t("app.name")}
					</span>
				</div>
				<h1 className="mb-4 font-semibold text-lg tracking-tight">{title}</h1>
				{children}
			</div>
		</div>
	);
}

/** 初始化令牌放在 URL 片段（`/setup#token=ost_…`），不进反代访问日志。 */
export function setupTokenFromHash(hash: string): string | undefined {
	return new URLSearchParams(hash.replace(/^#/, "")).get("token") || undefined;
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
	const [name, setName] = useState(() => navigator.platform || "Passkey");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [codes, setCodes] = useState<string[] | null>(null);

	const register = async () => {
		setBusy(true);
		setError(null);
		try {
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
			const res = await unwrap(
				api.POST("/api/v1/auth/setup/finish", {
					body: {
						ceremonyId: begin.ceremonyId,
						credential: credential as unknown as Record<string, unknown>,
					},
				}),
			);
			await qc.invalidateQueries({ queryKey: qk.me });
			if (res.recoveryCodes && res.recoveryCodes.length > 0)
				setCodes(res.recoveryCodes);
			else navigate("/");
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
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
				<ErrorText error={error} />
			</div>
		</AuthCard>
	);
}

export function LoginPage(): ReactNode {
	const { t } = useTranslation();
	const [params] = useSearchParams();
	const navigate = useNavigate();
	const qc = useQueryClient();
	const next = params.get("next") || "/";
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [recovery, setRecovery] = useState<string | null>(null);

	const login = async () => {
		setBusy(true);
		setError(null);
		try {
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
			navigate(next.startsWith("/") ? next : "/", { replace: true });
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	const submitRecovery = async () => {
		if (!recovery?.trim()) return;
		setBusy(true);
		setError(null);
		try {
			await unwrap(
				api.POST("/api/v1/auth/recovery", { body: { code: recovery.trim() } }),
			);
			navigate("/setup");
		} catch (e) {
			setError(e);
		} finally {
			setBusy(false);
		}
	};

	return (
		<AuthCard title={t("auth.loginTitle")}>
			<div className="flex flex-col gap-3">
				<Button variant="primary" disabled={busy} onClick={() => void login()}>
					{busy && recovery === null ? <Spinner /> : t("auth.loginWithPasskey")}
				</Button>
				{recovery === null ? (
					<button
						type="button"
						className="text-muted text-xs hover:text-fg"
						onClick={() => setRecovery("")}
					>
						{t("auth.useRecoveryCode")}
					</button>
				) : (
					<form
						className="flex flex-col gap-2"
						onSubmit={(e) => {
							e.preventDefault();
							void submitRecovery();
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
							{t("auth.recover")}
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
			<AuthCard title={t("pair.title")}>
				<Spinner />
			</AuthCard>
		);
	}
	if (me.isError) {
		return (
			<AuthCard title={t("pair.title")}>
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
		<AuthCard title={t("pair.title")}>
			<p className="mb-3 text-muted text-sm">{t("pair.hint")}</p>
			<PairApproval />
		</AuthCard>
	);
}
