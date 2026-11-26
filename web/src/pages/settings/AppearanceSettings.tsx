/**
 * 设置页「外观」（default export，由设置页接入）：主题卡（明暗、主题色、壁纸、App 图标）与语言卡。
 * 两张卡也各自具名导出。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type ChangeEvent,
	type CSSProperties,
	type ReactNode,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type LanguageCode,
	saveLanguageSettings,
	useLanguageSettings,
} from "../../api/language-queries";
import { radioKeys } from "../../components/settings/radioKeys";
import { useAsync } from "../../components/settings/useAsync";
import { Card } from "../../components/ui/data";
import { Icon, type IconName } from "../../components/ui/icons";
import { Button, ErrorText, Spinner } from "../../components/ui/primitives";
import { processWallpaper, readWallpaperImage } from "../../lib/wallpaper";
import {
	ACCENTS,
	type AccentName,
	MODES,
	type ThemeMode,
	themeStore,
	useTheme,
	WALLPAPERS,
} from "../../state/theme";
import { AppIconPicker } from "./AppIconPicker";

/**
 * 主题色色块：各预设 accent 在浅色 / 深色下的实际取值（与 index.css 的 `--accent-*` 一致），用 `light-dark()`
 * 随当前明暗显示。全站唯一允许的颜色字面量（另见 favicon）。
 */
const SWATCH: Record<AccentName, string> = {
	teal: "light-dark(oklch(0.53 0.105 205), oklch(0.78 0.105 205))",
	cobalt: "light-dark(oklch(0.53 0.17 258), oklch(0.78 0.17 258))",
	violet: "light-dark(oklch(0.53 0.17 295), oklch(0.78 0.17 295))",
	berry: "light-dark(oklch(0.53 0.17 358), oklch(0.78 0.17 358))",
	fox: "light-dark(oklch(0.55 0.16 48), oklch(0.78 0.16 48))",
	graphite: "light-dark(oklch(0.3 0.015 260), oklch(0.9 0.015 260))",
};

const MODE_ICON: Record<ThemeMode, IconName> = {
	system: "monitor",
	light: "sun",
	dark: "moon",
};

export function ThemeSettings() {
	const { t } = useTranslation();
	const mode = useTheme((s) => s.mode);
	const accent = useTheme((s) => s.accent);
	const setMode = useTheme((s) => s.setMode);
	const setAccent = useTheme((s) => s.setAccent);
	return (
		<Card title={t("appearance.theme")} icon="palette">
			<div className="flex flex-col gap-5">
				<div className="flex flex-col gap-2">
					<span aria-hidden className="font-medium text-muted text-xs">
						{t("appearance.mode")}
					</span>
					<div
						role="radiogroup"
						aria-label={t("appearance.mode")}
						onKeyDown={radioKeys}
						className="inline-flex w-fit max-w-full rounded-full border border-border bg-inset p-0.5"
					>
						{MODES.map((m) => {
							const on = mode === m;
							return (
								// biome-ignore lint/a11y/useSemanticElements: 分段按钮外观的单选（WAI-ARIA radio），原生 radio 画不出
								<button
									key={m}
									type="button"
									role="radio"
									aria-checked={on}
									tabIndex={on ? 0 : -1}
									onClick={() => setMode(m)}
									className={`inline-flex h-7 items-center gap-1.5 rounded-full px-3 font-medium text-xs transition-[background-color,color,box-shadow] duration-150 ${on ? "bg-panel text-fg shadow-xs ring-1 ring-border" : "text-muted hover:text-fg"}`}
								>
									<Icon name={MODE_ICON[m]} size={14} />
									{t(`appearance.modes.${m}`)}
								</button>
							);
						})}
					</div>
				</div>
				<div className="flex flex-col gap-2">
					<span aria-hidden className="font-medium text-muted text-xs">
						{t("appearance.accent")}
					</span>
					<div
						role="radiogroup"
						aria-label={t("appearance.accent")}
						onKeyDown={radioKeys}
						className="grid w-fit grid-cols-6 gap-x-1 gap-y-3 sm:gap-x-4"
					>
						{ACCENTS.map((a) => {
							const on = accent === a;
							const label = t(`appearance.accents.${a}`);
							return (
								// biome-ignore lint/a11y/useSemanticElements: 色块外观的单选（WAI-ARIA radio），原生 radio 画不出
								<button
									key={a}
									type="button"
									role="radio"
									aria-checked={on}
									aria-label={label}
									tabIndex={on ? 0 : -1}
									onClick={() => setAccent(a)}
									className={`flex w-12 flex-col items-center gap-1.5 rounded-md py-1 text-2xs transition-colors duration-150 ${on ? "font-medium text-fg" : "text-muted hover:text-fg"}`}
								>
									{/* 细内描边：色块和卡片底色接近时（深色下的石墨）也能一眼分辨边界 */}
									<span
										aria-hidden
										className={`grid h-7 w-7 place-items-center rounded-full text-ink-fg shadow-xs inset-ring-1 inset-ring-fg/20 ring-offset-2 ring-offset-panel transition-shadow duration-150 ${on ? "ring-2 ring-[var(--ring)]" : ""}`}
										style={{ background: SWATCH[a] }}
									>
										{on && <Icon name="check" size={14} weight="bold" />}
									</span>
									<span aria-hidden>{label}</span>
								</button>
							);
						})}
					</div>
				</div>
				<WallpaperPicker />
				<AppIconPicker />
			</div>
		</Card>
	);
}

const LANGUAGES: readonly { code: LanguageCode; label: string }[] = [
	{ code: "zh-CN", label: "简体中文" },
	{ code: "en", label: "English" },
];

export function LanguageSettingsCard() {
	const { t, i18n } = useTranslation();
	const qc = useQueryClient();
	const q = useLanguageSettings();
	const save = useAsync();
	const current: LanguageCode =
		q.data?.language ?? (i18n.language === "en" ? "en" : "zh-CN");
	const onPick = (code: LanguageCode) => {
		if (code === current && q.data?.language === code) return;
		void save.run(() => saveLanguageSettings(qc, code));
	};
	return (
		<Card title={t("language.title")} icon="globe">
			<div className="flex flex-col gap-3">
				<div className="flex flex-col gap-2">
					<span aria-hidden className="font-medium text-muted text-xs">
						{t("language.label")}
					</span>
					<div
						role="radiogroup"
						aria-label={t("language.title")}
						onKeyDown={radioKeys}
						className="inline-flex w-fit max-w-full rounded-full border border-border bg-inset p-0.5"
					>
						{LANGUAGES.map((item) => {
							const on = current === item.code;
							return (
								// biome-ignore lint/a11y/useSemanticElements: 分段按钮外观的单选（WAI-ARIA radio），原生 radio 画不出
								<button
									key={item.code}
									type="button"
									role="radio"
									aria-checked={on}
									tabIndex={on ? 0 : -1}
									disabled={save.busy}
									onClick={() => onPick(item.code)}
									className={`inline-flex h-7 items-center gap-1.5 rounded-full px-3 font-medium text-xs transition-[background-color,color,box-shadow] duration-150 ${on ? "bg-panel text-fg shadow-xs ring-1 ring-border" : "text-muted hover:text-fg"}`}
								>
									{item.label}
								</button>
							);
						})}
					</div>
				</div>
				<p className="text-faint text-xs">{t("language.hint")}</p>
				<ErrorText error={q.error ?? save.error} />
			</div>
		</Card>
	);
}

/**
 * 壁纸：内置三种 + 自定义图片。缩略卡挂 `.wp-<名>` 类，按当前主题色与明暗真实渲染；
 * 自定义卡的图片与纱厚度经内联变量传入（壁纸不是 custom 时 <html> 上没有这些变量）。
 * 还没有图片时「自定义」是上传入口而不是单选项，方向键不会停在它上面。
 */
function WallpaperPicker() {
	const { t } = useTranslation();
	const wallpaper = useTheme((s) => s.wallpaper);
	const tone = useTheme((s) => s.tone);
	const setWallpaper = useTheme((s) => s.setWallpaper);
	const [image, setImage] = useState(readWallpaperImage);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<Error | null>(null);
	const fileRef = useRef<HTMLInputElement>(null);
	const onFile = (e: ChangeEvent<HTMLInputElement>) => {
		const file = e.target.files?.[0];
		e.target.value = "";
		if (!file) return;
		setBusy(true);
		setError(null);
		// 代次记在 theme store 里：上传途中改选了别的壁纸，结果就作废；离开设置页（卸载）不影响
		const gen = themeStore.getState().beginWallpaperUpload();
		void processWallpaper(file)
			.then(({ url, tone }) => {
				// 存储失败时抛错，壁纸保持原样
				if (themeStore.getState().setCustomWallpaper(url, tone, gen))
					setImage(url);
			})
			.catch(() => {
				// 已被新的壁纸选择取代的上传，失败也不再提示
				if (themeStore.getState().wallpaperGen === gen)
					setError(new Error(t("appearance.wallpaperError")));
			})
			.finally(() => setBusy(false));
	};
	const thumb = (w: (typeof WALLPAPERS)[number], on: boolean) => (
		<span
			aria-hidden
			className={`wp-${w} block h-[60px] w-24 rounded-lg inset-ring-1 inset-ring-fg/15 ring-offset-2 ring-offset-panel transition-shadow duration-150 ${on ? "ring-2 ring-[var(--ring)]" : ""}`}
			style={
				w === "custom" && image
					? ({
							"--wallpaper-image": `url("${image}")`,
							"--wp-lo": tone?.[0] ?? 0.5,
							"--wp-hi": tone?.[1] ?? 0.5,
						} as CSSProperties)
					: undefined
			}
		/>
	);
	return (
		<div className="flex flex-col gap-2">
			<span aria-hidden className="font-medium text-muted text-xs">
				{t("appearance.wallpaper")}
			</span>
			<div
				role="radiogroup"
				aria-label={t("appearance.wallpaper")}
				onKeyDown={radioKeys}
				className="flex flex-wrap gap-x-3 gap-y-3"
			>
				{WALLPAPERS.map((w) => {
					const label = t(`appearance.wallpapers.${w}`);
					if (w === "custom" && !image)
						return (
							<button
								key={w}
								type="button"
								aria-label={`${label}：${t("appearance.wallpaperUpload")}`}
								disabled={busy}
								onClick={() => fileRef.current?.click()}
								className="flex flex-col items-center gap-1.5 rounded-md py-1 text-2xs text-muted transition-colors duration-150 hover:text-fg disabled:opacity-50"
							>
								<span
									aria-hidden
									className="grid h-[60px] w-24 place-items-center rounded-lg border border-border-strong border-dashed text-faint"
								>
									{busy ? <Spinner /> : <Icon name="upload" size={16} />}
								</span>
								<span aria-hidden>{label}</span>
							</button>
						);
					const on = wallpaper === w;
					return (
						// biome-ignore lint/a11y/useSemanticElements: 缩略卡外观的单选（WAI-ARIA radio），原生 radio 画不出
						<button
							key={w}
							type="button"
							role="radio"
							aria-checked={on}
							aria-label={label}
							tabIndex={on ? 0 : -1}
							onClick={() => setWallpaper(w)}
							className={`flex flex-col items-center gap-1.5 rounded-md py-1 text-2xs transition-colors duration-150 ${on ? "font-medium text-fg" : "text-muted hover:text-fg"}`}
						>
							{thumb(w, on)}
							<span aria-hidden>{label}</span>
						</button>
					);
				})}
			</div>
			{/* 没有图片时「自定义」磁贴就是上传入口；有图片后才出现「更换 / 移除」 */}
			<input
				ref={fileRef}
				type="file"
				accept="image/*"
				className="sr-only"
				tabIndex={-1}
				aria-hidden
				disabled={busy}
				onChange={onFile}
			/>
			{image && (
				<div className="flex flex-wrap items-center gap-2">
					<Button
						size="sm"
						icon="upload"
						disabled={busy}
						onClick={() => fileRef.current?.click()}
					>
						{t("appearance.wallpaperReplace")}
					</Button>
					<Button
						size="sm"
						variant="ghost"
						icon="trash"
						disabled={busy}
						onClick={() => {
							themeStore.getState().removeCustomWallpaper();
							setImage(null);
							setError(null);
						}}
					>
						{t("appearance.wallpaperRemove")}
					</Button>
				</div>
			)}
			<p className="text-faint text-xs">{t("appearance.wallpaperHint")}</p>
			<ErrorText error={error} />
		</div>
	);
}

export default function AppearanceSettings(): ReactNode {
	return (
		<>
			<ThemeSettings />
			<LanguageSettingsCard />
		</>
	);
}
