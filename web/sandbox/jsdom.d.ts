// 仓库没有装 @types/jsdom；沙箱运行时的测试只用到构造独立窗口这一小块，在这里声明。
declare module "jsdom" {
	export class JSDOM {
		constructor(html?: string, options?: { url?: string });
		readonly window: Window & typeof globalThis;
	}
}
