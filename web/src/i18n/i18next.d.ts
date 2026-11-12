import "i18next";
import type zhCN from "./locales/zh-CN.json";

// 以 zh-CN 为基准为 t() 提供 key 类型检查
declare module "i18next" {
	interface CustomTypeOptions {
		defaultNS: "translation";
		resources: { translation: typeof zhCN };
	}
}
