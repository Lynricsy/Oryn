/**
 * 设置 → 外观里的「App 图标」：只在宿主支持可替换图标时出现（iOS 移动壳，bridge ≥ 6）。
 * 缩略图用 BrandMark 的同一份字形按当前主题现画，与主屏上的图标（web/scripts/gen-icons.py 生成）一致：
 * 默认黑白，「品牌色」只把光标竖条换成品牌色。
 */
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { radioKeys } from "../../components/settings/GlyphGrid";
import { ErrorText, Spinner } from "../../components/ui/primitives";
import { type AppIconState, host } from "../../lib/host";

/** 页面认识的图标 id（壳的 `mobile_logic::APP_ICONS` 加上默认）；壳带了页面不认识的就不显示。 */
const KNOWN = ["default", "accent"] as const;
type KnownIcon = (typeof KNOWN)[number];

const DISC = "M20.1 4.52A12 12 0 1 0 20.1 27.48Z";
const CARET =
	"M24.5 4H25.9A1.1 1.1 0 0 1 27 5.1V26.9A1.1 1.1 0 0 1 25.9 28H24.5A1.1 1.1 0 0 1 23.4 26.9V5.1A1.1 1.1 0 0 1 24.5 4Z";

function Thumb({ id, on }: { id: KnownIcon; on: boolean }) {
	return (
		<span
			aria-hidden
			className={`block h-14 w-14 overflow-hidden rounded-[22%] bg-linear-to-b from-panel to-bg inset-ring-1 inset-ring-fg/15 ring-offset-2 ring-offset-panel transition-shadow duration-150 ${on ? "ring-2 ring-[var(--ring)]" : ""}`}
		>
			{/* 字形高占图标一半、外接框居中（中心 15.8,16），与 gen-icons.py 的 tile_svg 相同 */}
			<svg viewBox="-8.2 -8 48 48" className="h-full w-full" aria-hidden>
				<path d={DISC} fill="var(--text)" />
				<path
					d={CARET}
					fill={id === "accent" ? "var(--brand)" : "var(--text)"}
				/>
			</svg>
		</span>
	);
}

export function AppIconPicker() {
	const { t } = useTranslation();
	const api = host.mobile?.appIcon ?? null;
	const [state, setState] = useState<AppIconState | null>(null);
	const [busy, setBusy] = useState<KnownIcon | null>(null);
	const [error, setError] = useState<Error | null>(null);

	useEffect(() => {
		if (!api) return;
		let live = true;
		api.get().then(
			(s) => live && setState(s),
			// 读不到就不显示这一项（壳太旧或系统不允许），不打扰用户
			() => undefined,
		);
		return () => {
			live = false;
		};
	}, [api]);

	const options = KNOWN.filter((id) => state?.options.includes(id));
	if (!api || !state?.supported || options.length < 2) return null;

	const choose = (id: KnownIcon) => {
		if (busy || id === state.current) return;
		setBusy(id);
		setError(null);
		api
			.set(id)
			.then(
				() => setState((s) => (s ? { ...s, current: id } : s)),
				(e: unknown) =>
					setError(
						new Error(
							t("appearance.appIconError", {
								message: e instanceof Error ? e.message : String(e),
							}),
						),
					),
			)
			.finally(() => setBusy(null));
	};

	return (
		<div className="flex flex-col gap-2" data-testid="app-icon-picker">
			<span aria-hidden className="font-medium text-muted text-xs">
				{t("appearance.appIcon")}
			</span>
			<div
				role="radiogroup"
				aria-label={t("appearance.appIcon")}
				onKeyDown={radioKeys}
				className="flex flex-wrap gap-x-4 gap-y-3"
			>
				{options.map((id) => {
					const on = state.current === id;
					const label = t(`appearance.appIcons.${id}`);
					return (
						// biome-ignore lint/a11y/useSemanticElements: 缩略图外观的单选（WAI-ARIA radio），原生 radio 画不出
						<button
							key={id}
							type="button"
							role="radio"
							aria-checked={on}
							aria-label={label}
							tabIndex={on ? 0 : -1}
							disabled={busy !== null}
							onClick={() => choose(id)}
							className={`relative flex flex-col items-center gap-1.5 rounded-md py-1 text-2xs transition-colors duration-150 disabled:cursor-default ${on ? "font-medium text-fg" : "text-muted hover:text-fg"}`}
						>
							<Thumb id={id} on={on} />
							{busy === id && (
								<span className="absolute top-1 grid h-14 w-14 place-items-center">
									<Spinner />
								</span>
							)}
							<span aria-hidden>{label}</span>
						</button>
					);
				})}
			</div>
			<p className="text-faint text-xs">{t("appearance.appIconHint")}</p>
			<ErrorText error={error} />
		</div>
	);
}
