/**
 * 桌面壳的连接页（M6 计划 4）：壳 `session.rs` 状态机的投影。外观与 `/login`、`/pair` 一致（共用 AuthCard）。
 * - connect：填服务器地址 → `launcher_connect`；错误按 `error.code` 给出原因与下一步。
 * - pairing：配对码（等宽大字 + 复制）、剩余有效时间、重新打开浏览器 / 取消。
 * - connecting / connected：转圈 + 服务器主机名。
 * - error：原因与建议、重试、更换服务器（先在本页确认）；`unreachable` 时壳在自动重连。
 * - confirmChange：确认移除这台设备在该服务器上的登录。
 * 每条命令都返回新的状态；壳也会经 `oryn:host` 事件主动推送（轮询批准、自动重连）。
 */
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AuthCard } from "../components/AuthCard";
import { CopyButton } from "../components/ui/CopyButton";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextInput,
} from "../components/ui/primitives";
import {
	invoke,
	LAUNCHER_ERROR_CODES,
	type LauncherError,
	type LauncherErrorCode,
	type LauncherInfo,
	type LauncherState,
	onState,
} from "./bridge";

/** URL 的主机部分（带端口）；解析不了时原样返回。 */
function hostOf(url: string): string {
	try {
		return new URL(url).host || url;
	} catch {
		return url;
	}
}

/** 已知错误码；壳以后新增的码按 unknown 处理。 */
function launcherErrorKey(code: string): LauncherErrorCode | "unknown" {
	return (LAUNCHER_ERROR_CODES as readonly string[]).includes(code)
		? (code as LauncherErrorCode)
		: "unknown";
}

/** 剩余时间 `m:ss`。 */
function formatRemaining(ms: number): string {
	const s = Math.max(0, Math.ceil(ms / 1000));
	return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** 原因（ErrorText）+ 下一步；壳的原文只放在悬停提示里备查。 */
function LauncherErrorText({ error }: { error: LauncherError }) {
	const { t } = useTranslation();
	const key = launcherErrorKey(error.code);
	return (
		<div className="flex flex-col gap-1" title={error.message || undefined}>
			<ErrorText error={t(`launcher.error.${key}.reason`)} />
			<p className="text-faint text-xs">{t(`launcher.error.${key}.hint`)}</p>
		</div>
	);
}

function PairingCountdown({ expiresAt }: { expiresAt: number }) {
	const { t } = useTranslation();
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		const id = setInterval(() => setNow(Date.now()), 1000);
		return () => clearInterval(id);
	}, []);
	return (
		<span className="tabular-nums">
			{t("launcher.expiresIn", { time: formatRemaining(expiresAt - now) })}
		</span>
	);
}

export function Launcher(): ReactNode {
	const { t, i18n } = useTranslation();
	const [info, setInfo] = useState<LauncherInfo | null>(null);
	const [state, setState] = useState<LauncherState | null>(null);
	const [url, setUrl] = useState("");
	const [busy, setBusy] = useState(false);
	/** IPC 本身失败（不是壳给出的业务错误）。 */
	const [failure, setFailure] = useState<unknown>(null);
	/** 在错误页点了「更换服务器…」，等本页确认。 */
	const [confirmLocal, setConfirmLocal] = useState(false);

	useEffect(() => {
		let live = true;
		const off = onState((s) => setState(s));
		invoke<LauncherInfo>("launcher_state").then(
			(r) => {
				if (!live) return;
				setInfo(r);
				setState(r.state);
				if (r.lang && r.lang !== i18n.language)
					void i18n.changeLanguage(r.lang);
			},
			(e) => {
				if (live) setFailure(e);
			},
		);
		return () => {
			live = false;
			off();
		};
	}, [i18n]);

	useEffect(() => {
		document.documentElement.lang = i18n.language;
	}, [i18n.language]);

	// 回到 connect（取消、过期、吊销）时预填壳记住的地址
	const prefill = state?.step === "connect" ? state.serverUrl : null;
	useEffect(() => {
		if (prefill) setUrl(prefill);
	}, [prefill]);

	const run = async (cmd: string, args?: Record<string, unknown>) => {
		setBusy(true);
		setFailure(null);
		try {
			setState(await invoke<LauncherState>(cmd, args));
			setConfirmLocal(false);
		} catch (e) {
			setFailure(e);
		} finally {
			setBusy(false);
		}
	};

	const submit = (e: FormEvent) => {
		e.preventDefault();
		if (!url.trim() || busy) return;
		void run("launcher_connect", { url: url.trim() });
	};

	const footer = info && (
		<>
			{info.tokenStore === "file" && <p>{t("launcher.fileStore")}</p>}
			<p className="font-mono">
				{t("launcher.version", { version: info.version })}
			</p>
		</>
	);

	if (!state) {
		return (
			<AuthCard title={t("launcher.title.connecting")} footer={footer}>
				{failure ? <ErrorText error={failure} /> : <Spinner />}
			</AuthCard>
		);
	}

	const server = hostOf(state.serverUrl);

	if (state.step === "confirmChange" || confirmLocal) {
		return (
			<AuthCard title={t("launcher.title.confirmChange")} footer={footer}>
				<p className="mb-4 text-muted text-sm">
					{t("launcher.changeHint", { host: server })}
				</p>
				<div className="flex flex-wrap gap-2">
					<Button
						variant="danger"
						disabled={busy}
						onClick={() =>
							void run("launcher_change_server", { confirm: true })
						}
					>
						{t("launcher.changeConfirm")}
					</Button>
					<Button
						variant="ghost"
						disabled={busy}
						onClick={() =>
							confirmLocal
								? setConfirmLocal(false)
								: void run("launcher_change_server", { confirm: false })
						}
					>
						{t("common.cancel")}
					</Button>
				</div>
				<div className="mt-3">
					<ErrorText error={failure} />
				</div>
			</AuthCard>
		);
	}

	if (state.step === "connect") {
		return (
			<AuthCard title={t("launcher.title.connect")} blink footer={footer}>
				<p className="mb-4 text-muted text-sm">{t("launcher.intro")}</p>
				<form className="flex flex-col gap-3" onSubmit={submit}>
					<Field
						label={t("launcher.serverUrl")}
						hint={t("launcher.serverUrlHint")}
					>
						<TextInput
							autoFocus
							inputMode="url"
							autoComplete="url"
							autoCapitalize="off"
							spellCheck={false}
							placeholder="https://oryn.example.com"
							value={url}
							onChange={(e) => setUrl(e.target.value)}
						/>
					</Field>
					<Button
						type="submit"
						variant="primary"
						disabled={busy || !url.trim()}
					>
						{busy ? <Spinner /> : t("launcher.connect")}
					</Button>
					{state.error && <LauncherErrorText error={state.error} />}
					<ErrorText error={failure} />
				</form>
			</AuthCard>
		);
	}

	if (state.step === "pairing") {
		return (
			<AuthCard title={t("launcher.title.pairing")} blink footer={footer}>
				<p className="mb-4 text-muted text-sm">
					{t("launcher.pairingHint", { host: hostOf(state.verifyUrl) })}
				</p>
				<div className="mb-2 flex items-center justify-center rounded-md bg-inset px-3 py-4">
					<output
						aria-label={t("launcher.code")}
						className="select-all font-mono font-semibold text-2xl text-fg tracking-widest tabular-nums"
					>
						{state.code}
					</output>
				</div>
				<div className="mb-4 flex items-center justify-between gap-2 text-faint text-xs">
					<span className="inline-flex items-center gap-1.5">
						<Spinner size={11} />
						{t("launcher.waiting")}
						<span aria-hidden>·</span>
						<PairingCountdown expiresAt={state.expiresAt} />
					</span>
					<CopyButton text={state.code} label={t("common.copy")} />
				</div>
				<div className="flex flex-wrap gap-2">
					<Button
						disabled={busy}
						onClick={() => void run("launcher_open_browser")}
					>
						{t("launcher.reopenBrowser")}
					</Button>
					<Button
						variant="ghost"
						disabled={busy}
						onClick={() => void run("launcher_cancel")}
					>
						{t("common.cancel")}
					</Button>
				</div>
				<div className="mt-3">
					<ErrorText error={failure} />
				</div>
			</AuthCard>
		);
	}

	if (state.step === "error") {
		return (
			<AuthCard title={t("launcher.title.error")} footer={footer}>
				<p className="mb-3 font-mono text-muted text-xs">{server}</p>
				<LauncherErrorText error={state.error} />
				{state.error.code === "unreachable" && (
					<p className="mt-2 inline-flex items-center gap-1.5 text-muted text-xs">
						<Spinner size={11} />
						{t("launcher.reconnecting")}
					</p>
				)}
				<div className="mt-4 flex flex-wrap gap-2">
					<Button
						variant="primary"
						disabled={busy}
						onClick={() => void run("launcher_retry")}
					>
						{busy ? <Spinner /> : t("launcher.retry")}
					</Button>
					<Button
						variant="ghost"
						disabled={busy}
						onClick={() => setConfirmLocal(true)}
					>
						{t("launcher.changeServer")}
					</Button>
				</div>
				<div className="mt-3">
					<ErrorText error={failure} />
				</div>
			</AuthCard>
		);
	}

	// connecting / connected：主窗口马上会离开本页
	return (
		<AuthCard title={t("launcher.title.connecting")} footer={footer}>
			<p className="inline-flex items-center gap-2 text-muted text-sm">
				<Spinner />
				{t("launcher.connectingTo", { host: server })}
			</p>
		</AuthCard>
	);
}
