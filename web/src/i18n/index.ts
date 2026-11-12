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
	return navigator.languages?.length
		? navigator.languages
		: [navigator.language];
}

const initialLocale = detectLocale(systemLanguages());

void i18n.use(initReactI18next).init({
	resources,
	lng: initialLocale,
	fallbackLng: fallbackLocale,
	interpolation: { escapeValue: false },
});

if (typeof document !== "undefined") {
	document.documentElement.lang = initialLocale;
}

export default i18n;
