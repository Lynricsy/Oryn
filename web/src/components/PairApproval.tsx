/**
 * 设备配对批准：所有者输入设备上显示的配对码 → `GET devices/pair/{code}` 查看发起设备（名称、IP、UA、时间）→
 * 确认后 `POST devices/pair/approve`。404 = 无效或已过期，409 = 已批准。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, api, type Schemas, unwrap } from "../api/client";
import { qk } from "../api/queries";
import { formatDateTime, formatRelative } from "../lib/format";
import { Button, ErrorText, Spinner, TextInput } from "./ui/primitives";

/** 统一配对码格式：去空白、大写、补上中间的连字符（ABCD-EFGH）。 */
export function normalizePairCode(raw: string): string {
	const s = raw.replace(/[\s-]/g, "").toUpperCase();
	return s.length === 8 ? `${s.slice(0, 4)}-${s.slice(4)}` : s;
}

export function PairApproval({
	initialCode = "",
}: {
	initialCode?: string;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const [code, setCode] = useState(initialCode);
	const [pairing, setPairing] = useState<Schemas["DevicePairing"] | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [done, setDone] = useState(false);

	const explain = (e: unknown): unknown => {
		if (e instanceof ApiError && e.status === 404)
			return new Error(t("pair.notFound"));
		if (e instanceof ApiError && e.status === 409)
			return new Error(t("pair.alreadyApproved"));
		return e;
	};

	const lookup = async () => {
		setBusy(true);
		setError(null);
		setDone(false);
		try {
			const c = normalizePairCode(code);
			setPairing(
				await unwrap(
					api.GET("/api/v1/devices/pair/{code}", {
						params: { path: { code: c } },
					}),
				),
			);
		} catch (e) {
			setPairing(null);
			setError(explain(e));
		} finally {
			setBusy(false);
		}
	};

	const approve = async () => {
		if (!pairing) return;
		setBusy(true);
		setError(null);
		try {
			await unwrap(
				api.POST("/api/v1/devices/pair/approve", {
					body: { code: pairing.code },
				}),
			);
			setDone(true);
			setPairing(null);
			setCode("");
			await qc.invalidateQueries({ queryKey: qk.devices });
		} catch (e) {
			setError(explain(e));
		} finally {
			setBusy(false);
		}
	};

	return (
		<div className="flex flex-col gap-3">
			<form
				className="flex flex-wrap gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					void lookup();
				}}
			>
				<TextInput
					aria-label={t("pair.code")}
					className="max-w-48 font-mono uppercase tracking-widest"
					placeholder="ABCD-EFGH"
					autoComplete="off"
					value={code}
					onChange={(e) => {
						setCode(e.target.value);
						setPairing(null);
					}}
				/>
				<Button
					type="submit"
					disabled={busy || normalizePairCode(code).length !== 9}
				>
					{busy && !pairing ? <Spinner /> : t("pair.lookup")}
				</Button>
			</form>
			{pairing && (
				<div className="rounded-lg border border-border bg-raised p-3 text-sm">
					<dl className="grid grid-cols-[6rem_1fr] gap-y-1">
						<dt className="text-muted">{t("pair.device")}</dt>
						<dd className="font-medium">{`${pairing.name} · ${t(`devices.kind.${pairing.kind}`)} · ${pairing.platform}`}</dd>
						<dt className="text-muted">IP</dt>
						<dd className="font-mono text-xs">{pairing.ip}</dd>
						<dt className="text-muted">{t("pair.userAgent")}</dt>
						<dd className="break-all text-xs">{pairing.userAgent}</dd>
						<dt className="text-muted">{t("pair.requestedAt")}</dt>
						<dd className="text-xs">{formatDateTime(pairing.createdAt)}</dd>
						<dt className="text-muted">{t("pair.expires")}</dt>
						<dd className="text-xs">{formatRelative(pairing.expiresAt)}</dd>
					</dl>
					<p className="mt-2 text-muted text-xs">{t("pair.verifyHint")}</p>
					<div className="mt-3 flex gap-2">
						<Button
							variant="primary"
							disabled={busy}
							onClick={() => void approve()}
						>
							{busy ? <Spinner /> : t("pair.approve")}
						</Button>
						<Button onClick={() => setPairing(null)}>
							{t("common.cancel")}
						</Button>
					</div>
				</div>
			)}
			{done && <p className="text-ok text-sm">{t("pair.approved")}</p>}
			<ErrorText error={error} />
		</div>
	);
}
