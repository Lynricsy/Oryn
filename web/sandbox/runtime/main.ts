// runtime.js 的入口（IIFE）：服务端把它内联在页面最前面，紧接着调用 `__orynStart(<配置 JSON>)`。

import { install } from "./start";

// 在页面任何脚本之前缓存父窗口引用
const parentWindow = window.parent;

// 只认第一次调用；调用后立即移除，页面脚本拿不到它
Object.defineProperty(window, "__orynStart", {
	configurable: true,
	enumerable: false,
	writable: false,
	value(config: unknown) {
		Reflect.deleteProperty(window, "__orynStart");
		install(window, parentWindow, config);
	},
});
