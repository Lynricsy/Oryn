/// <reference types="vite/client" />

interface ImportMetaEnv {
	/** `1` = 启用浏览器内 mock 服务端（无需后端即可体验界面）。 */
	readonly VITE_MOCK?: string;
}
