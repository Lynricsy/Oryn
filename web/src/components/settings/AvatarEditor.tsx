/**
 * 头像编辑：上传图片（先进裁剪对话框）、恢复 / 清除、内置图标弹层（emoji 与 Nerd 字形，可搜索）、自定义字形。
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
import { type FeaturedGlyphs, GlyphPicker } from "./GlyphPicker";

const AVATAR_FEATURED: FeaturedGlyphs = {
	icons: AVATAR_NERD,
	emoji: AVATAR_EMOJI,
};

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
	/** 裁剪中的源图；`key` = 它的解码代次，对话框按它换 key（换源时取景框按新图重新初始化） */
	const [crop, setCrop] = useState<{
		source: AvatarSource;
		key: number;
	} | null>(null);
	const [decodeFailed, setDecodeFailed] = useState(false);
	const sourceRef = useRef(crop?.source);
	sourceRef.current = crop?.source;
	// 解码代次：每次选图 +1，只有仍是最新代次、且编辑器仍挂载时完成的解码才生效
	const gen = useRef(0);
	const mounted = useRef(true);
	useEffect(() => {
		mounted.current = true;
		// 编辑器在对话框开着时被卸载（离开页面）：释放源图
		return () => {
			mounted.current = false;
			sourceRef.current?.release();
		};
	}, []);
	const isSet = Boolean(value?.avatarIcon || value?.avatarImage);

	const onFile = (e: ChangeEvent<HTMLInputElement>) => {
		const file = e.target.files?.[0];
		e.target.value = "";
		if (!file) return;
		const my = ++gen.current;
		setDecodeFailed(false);
		loadAvatarSource(file).then(
			(next) => {
				// 过期（已有更新的选择）或已卸载：丢弃并释放
				if (!mounted.current || my !== gen.current) return next.release();
				// 替换一个仍开着的源：旧的由这里释放（对话框只释放它自己确认 / 取消的那张）
				sourceRef.current?.release();
				setCrop({ source: next, key: my });
			},
			() => {
				// 过期的失败不覆盖更新的选择
				if (mounted.current && my === gen.current) setDecodeFailed(true);
			},
		);
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
				<GlyphPicker
					value={value?.avatarIcon}
					featured={AVATAR_FEATURED}
					defaultTab="emoji"
					label={t("appearance.avatarGlyphs")}
					disabled={busy}
					onPick={(avatarIcon) => void onChange({ avatarIcon })}
					triggerClassName="inline-flex h-7 select-none items-center gap-1 rounded-md border border-border bg-panel px-2.5 font-medium text-fg text-xs shadow-xs transition-colors duration-150 hover:bg-hover disabled:pointer-events-none disabled:opacity-50 aria-expanded:bg-hover"
				>
					<Icon name="shapes" size={13} />
					{t("appearance.avatarGlyphs")}
				</GlyphPicker>
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
			{crop && (
				<AvatarCropDialog
					key={crop.key}
					source={crop.source}
					onCancel={() => setCrop(null)}
					onConfirm={(avatarImage) => {
						setCrop(null);
						void onChange({ avatarImage });
					}}
				/>
			)}
		</div>
	);
}
