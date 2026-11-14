/**
 * 头像编辑：上传图片（先进裁剪对话框）、恢复 / 清除、emoji 与 Nerd 字形网格、自定义字形。
 * 不含卡片外框与预览；Agent、全局助手、role、我的头像四个入口共用，保存与错误显示由调用方负责。
 */
import {
	type ChangeEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import { type AvatarSource, loadAvatarSource } from "../../lib/avatar-image";
import { AVATAR_EMOJI, AVATAR_NERD, isSingleGlyph } from "../../lib/glyphs";
import { Icon } from "../ui/icons";
import { Button, Field, TextInput } from "../ui/primitives";
import { AvatarCropDialog } from "./AvatarCropDialog";
import { GlyphGrid } from "./GlyphGrid";

export function AvatarEditor({
	value,
	onChange,
	busy,
	resetLabel,
}: {
	value: Schemas["AvatarIdentity"] | undefined;
	/** `{}` = 清除；返回是否保存成功 */
	onChange: (next: Schemas["AvatarIdentity"]) => Promise<boolean>;
	busy: boolean;
	/** 「恢复」按钮的文案 */
	resetLabel: string;
}): ReactNode {
	const { t } = useTranslation();
	const [custom, setCustom] = useState("");
	const [source, setSource] = useState<AvatarSource | null>(null);
	const [decodeFailed, setDecodeFailed] = useState(false);
	// 编辑器在对话框开着时被卸载（离开页面）：释放源图
	const sourceRef = useRef(source);
	sourceRef.current = source;
	useEffect(() => () => sourceRef.current?.release(), []);
	const isSet = Boolean(value?.avatarIcon || value?.avatarImage);

	const onFile = (e: ChangeEvent<HTMLInputElement>) => {
		const file = e.target.files?.[0];
		e.target.value = "";
		if (!file) return;
		setDecodeFailed(false);
		loadAvatarSource(file).then(setSource, () => setDecodeFailed(true));
	};

	return (
		<div className="flex flex-col gap-3">
			<div className="flex flex-wrap items-center gap-2">
				<label className="inline-flex h-7 cursor-pointer select-none items-center gap-1 rounded-md border border-border bg-panel px-2.5 font-medium text-fg text-xs shadow-xs transition-colors duration-150 hover:bg-hover has-[:disabled]:pointer-events-none has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-[var(--ring)] has-[:focus-visible]:outline-offset-2 has-[:disabled]:opacity-50">
					<Icon name="upload" size={13} />
					{t("appearance.avatarUpload")}
					<input
						type="file"
						accept="image/png,image/webp,image/jpeg"
						className="sr-only"
						disabled={busy}
						onChange={onFile}
					/>
				</label>
				<Button
					size="sm"
					variant="ghost"
					icon="refresh"
					disabled={!isSet || busy}
					onClick={() => void onChange({})}
				>
					{resetLabel}
				</Button>
			</div>
			{decodeFailed && (
				<p role="alert" className="text-err text-xs">
					{t("appearance.avatarDecodeFailed")}
				</p>
			)}
			{/* 彩色 emoji 与单色字形分两组，不交替混排；外层保留整体的组名 */}
			<fieldset
				aria-label={t("appearance.avatarGlyphs")}
				className="m-0 flex min-w-0 flex-col gap-2 border-0 p-0"
			>
				<span aria-hidden className="font-medium text-muted text-xs">
					{t("appearance.avatarGlyphs")}
				</span>
				{(
					[
						["avatarEmoji", AVATAR_EMOJI],
						["avatarNerd", AVATAR_NERD],
					] as const
				).map(([key, glyphs]) => (
					<div key={key} className="flex flex-col gap-1">
						<span aria-hidden className="text-2xs text-faint">
							{t(`appearance.${key}`)}
						</span>
						<GlyphGrid
							glyphs={glyphs}
							value={value?.avatarIcon}
							label={t(`appearance.${key}`)}
							disabled={busy}
							onPick={(avatarIcon) => void onChange({ avatarIcon })}
						/>
					</div>
				))}
			</fieldset>
			<form
				className="flex items-end gap-2"
				onSubmit={(e) => {
					e.preventDefault();
					const avatarIcon = custom.trim();
					if (!isSingleGlyph(avatarIcon)) return;
					void onChange({ avatarIcon }).then((ok) => {
						if (ok) setCustom("");
					});
				}}
			>
				<Field label={t("appearance.avatarCustom")}>
					<TextInput
						className="w-40 text-center"
						maxLength={16}
						placeholder={t("appearance.avatarCustomPlaceholder")}
						value={custom}
						onChange={(e) => setCustom(e.target.value)}
					/>
				</Field>
				<Button type="submit" disabled={busy || !isSingleGlyph(custom)}>
					{t("appearance.avatarUse")}
				</Button>
			</form>
			{source && (
				<AvatarCropDialog
					source={source}
					onCancel={() => setSource(null)}
					onConfirm={(avatarImage) => {
						setSource(null);
						void onChange({ avatarImage });
					}}
				/>
			)}
		</div>
	);
}
