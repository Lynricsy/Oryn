import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import en from "./locales/en.json";
import zhCN from "./locales/zh-CN.json";

export const resources = {
	"zh-CN": { translation: zhCN },
	en: { translation: en },
} as const;

export type Locale = keyof typeof resources;

export const fallbackLocale: Locale = "zh-CN";

/** 按系统语言偏好挑选第一个受支持的界面语言；都不匹配时回退到 zh-CN。 */
export function detectLocale(preferred: readonly string[]): Locale {
	for (const tag of preferred) {
		const lower = tag.toLowerCase();
		if (lower.startsWith("zh")) return "zh-CN";
		if (lower.startsWith("en")) return "en";
	}
	return fallbackLocale;
}

function systemLanguages(): readonly string[] {
	if (typeof navigator === "undefined") return [];
	if (navigator.languages?.length) return navigator.languages;
	// Bun 的 navigator 没有语言（截图等脚本经 mock 间接导入本模块）
	return navigator.language ? [navigator.language] : [];
}

const LANGUAGE_STORAGE_KEY = "oryn.language";

function isLocale(value: string | null | undefined): value is Locale {
	return value === "zh-CN" || value === "en";
}

function readStoredLocale(): Locale | null {
	if (typeof localStorage === "undefined") return null;
	try {
		const v = localStorage.getItem(LANGUAGE_STORAGE_KEY);
		return isLocale(v) ? v : null;
	} catch {
		return null;
	}
}

function writeStoredLocale(lng: string): void {
	if (typeof localStorage === "undefined" || !isLocale(lng)) return;
	try {
		localStorage.setItem(LANGUAGE_STORAGE_KEY, lng);
	} catch {
		// 隐私模式或存储受限时忽略本地缓存
	}
}

const initialLocale = readStoredLocale() ?? detectLocale(systemLanguages());

void i18n.use(initReactI18next).init({
	resources,
	lng: initialLocale,
	fallbackLng: fallbackLocale,
	interpolation: { escapeValue: false },
});

if (typeof document !== "undefined") {
	document.documentElement.lang = initialLocale;
}

i18n.on("languageChanged", (lng) => {
	if (typeof document !== "undefined") {
		document.documentElement.lang = lng;
	}
	writeStoredLocale(lng);
});

export default i18n;
