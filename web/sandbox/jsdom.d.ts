// 仓库没有装 @types/jsdom；沙箱运行时的测试只用到构造独立窗口这一小块，在这里声明。
declare module "jsdom" {
	export class VirtualConsole {
		on(event: "jsdomError", listener: (err: Error) => void): this;
	}
	export class JSDOM {
		constructor(
			html?: string,
			options?: { url?: string; virtualConsole?: VirtualConsole },
		);
		readonly window: Window & typeof globalThis;
	}
}
