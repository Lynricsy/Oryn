// 由 web/scripts/gen-icons.py 生成，勿手改：设置 → 外观里 App 图标缩略图的配色，与主屏图标同源。
export const APP_ICON_PALETTE = {
	default: {
		light: {
			top: "#fcfeff",
			bottom: "#dfe8ed",
			ink: "#121a1e",
			caret: "#121a1e",
		},
		dark: {
			top: "#20282c",
			bottom: "#0a0f12",
			ink: "#e7ecef",
			caret: "#e7ecef",
		},
	},
	accent: {
		light: {
			top: "#fcfeff",
			bottom: "#dfe8ed",
			ink: "#121a1e",
			caret: "#007c89",
		},
		dark: {
			top: "#20282c",
			bottom: "#0a0f12",
			ink: "#e7ecef",
			caret: "#57cbd7",
		},
	},
} as const;
