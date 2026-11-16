// 运行时操作页面 DOM 用的原生函数：在页面自己的脚本之前从页面窗口取出并缓存，之后只经 `Reflect.apply` 调用。
// 页面脚本可以改写 `Node.prototype` 等原型上的方法；若运行时照常调用 `root.appendChild(…)`，被改写的方法就会
// 拿到 `this`（例如 closed shadow root 里的节点）。只用这里缓存的函数，页面改写原型不影响运行时、也拿不到覆盖层内部。

const apply = Reflect.apply;
const describe = Object.getOwnPropertyDescriptor;
const getPrototypeOf = Object.getPrototypeOf;

/** 页面加载前缓存的 `Date.now`（模块随 runtime.js 在页面脚本之前求值）。 */
export const now: () => number = Date.now;

/** 运行时用到的 DOM 原生操作；`self` 参数是 `this`。 */
export interface Dom {
	readonly win: Window;
	readonly doc: Document;
	readonly HTMLElement: typeof HTMLElement;
	readonly MutationObserver: typeof MutationObserver;
	observe(
		self: MutationObserver,
		target: Node,
		init: MutationObserverInit,
	): void;
	defineElement(name: string, ctor: CustomElementConstructor): void;
	getComputedStyle(el: Element): CSSStyleDeclaration;
	/** 页面视口坐标处最上层的元素（`pointer-events: none` 的覆盖层不算）；环境不支持时为 `null`。 */
	elementFromPoint(x: number, y: number): Element | null;
	/** 下一帧回调；没有 `requestAnimationFrame` 时退回 16 ms 的计时器。 */
	frame(cb: () => void): void;
	createElement(self: Document, tag: string): HTMLElement;
	querySelectorAll(self: Document, selector: string): NodeList;
	documentElement(self: Document): Element | null;
	listLength(self: NodeList): number;
	listItem(self: NodeList, i: number): Node | null;
	appendChild(self: Node, child: Node): Node;
	parentNode(self: Node): Node | null;
	firstChild(self: Node): Node | null;
	nextSibling(self: Node): Node | null;
	nodeType(self: Node): number;
	nodeValue(self: Node): string | null;
	isConnected(self: Node): boolean;
	getText(self: Node): string | null;
	setText(self: Node, text: string): void;
	localName(self: Element): string;
	getAttribute(self: Element, name: string): string | null;
	setAttribute(self: Element, name: string, value: string): void;
	removeAttribute(self: Element, name: string): void;
	rect(self: Element): DOMRect;
	attachShadow(self: Element, init: ShadowRootInit): ShadowRoot;
	/** 只对 HTML 元素可用；别的元素（SVG 等）抛 `TypeError`。 */
	style(self: Element): CSSStyleDeclaration;
	setProperty(self: CSSStyleDeclaration, prop: string, value: string): void;
	getPropertyValue(self: CSSStyleDeclaration, prop: string): string;
	preventDefault(self: Event): void;
	stopImmediatePropagation(self: Event): void;
	target(self: Event): EventTarget | null;
	clientX(self: MouseEvent): number;
	clientY(self: MouseEvent): number;
	relatedTarget(self: MouseEvent): EventTarget | null;
}

// 沿原型链找到 `name` 的属性描述（getter 可能定义在更上层的原型上）
function lookup(proto: object, name: string): PropertyDescriptor {
	let p: object | null = proto;
	while (p) {
		const d = describe(p, name);
		if (d) return d;
		p = getPrototypeOf(p);
	}
	throw new TypeError(`缺少 ${name}`);
}

function method<S, A extends unknown[], R>(
	proto: object,
	name: string,
): (self: S, ...args: A) => R {
	const fn = lookup(proto, name).value as (...args: A) => R;
	return (self, ...args) => apply(fn, self, args) as R;
}

function getter<S, R>(proto: object, name: string): (self: S) => R {
	const fn = lookup(proto, name).get as () => R;
	return (self) => apply(fn, self, []) as R;
}

function setter<S, V>(proto: object, name: string): (self: S, v: V) => void {
	const fn = lookup(proto, name).set as (v: V) => void;
	return (self, v) => {
		apply(fn, self, [v]);
	};
}

/** 页面窗口里的构造函数（页面所在的 realm，测试里是另一个 jsdom 窗口）。 */
type Realm = Window & typeof globalThis;

/** 从页面窗口 `win` 取出运行时要用的 DOM 原生函数。必须在页面脚本运行之前调用。 */
export function captureDom(win: Window): Dom {
	const w = win as Realm;
	const doc = win.document;
	const node = w.Node.prototype;
	const element = w.Element.prototype;
	const document = w.Document.prototype;
	const css = w.CSSStyleDeclaration.prototype;
	const event = w.Event.prototype;
	const mouse = w.MouseEvent.prototype;
	const list = w.NodeList.prototype;
	const fromPoint = doc.elementFromPoint as
		| ((x: number, y: number) => Element | null)
		| undefined;
	const raf = win.requestAnimationFrame as
		| ((cb: FrameRequestCallback) => number)
		| undefined;
	const setTimer = win.setTimeout.bind(win);
	const registry = win.customElements;
	const define = registry.define;
	const computed = win.getComputedStyle;
	return {
		win,
		doc,
		HTMLElement: w.HTMLElement,
		MutationObserver: w.MutationObserver,
		observe: method(w.MutationObserver.prototype, "observe"),
		defineElement: (name, ctor) => {
			apply(define, registry, [name, ctor]);
		},
		getComputedStyle: (el) => apply(computed, win, [el]) as CSSStyleDeclaration,
		elementFromPoint: (x, y) =>
			typeof fromPoint === "function"
				? (apply(fromPoint, doc, [x, y]) as Element | null)
				: null,
		frame: (cb) => {
			if (typeof raf === "function") apply(raf, win, [cb]);
			else setTimer(cb, 16);
		},
		createElement: method(document, "createElement"),
		querySelectorAll: method(document, "querySelectorAll"),
		documentElement: getter(document, "documentElement"),
		listLength: getter(list, "length"),
		listItem: method(list, "item"),
		appendChild: method(node, "appendChild"),
		parentNode: getter(node, "parentNode"),
		firstChild: getter(node, "firstChild"),
		nextSibling: getter(node, "nextSibling"),
		nodeType: getter(node, "nodeType"),
		nodeValue: getter(node, "nodeValue"),
		isConnected: getter(node, "isConnected"),
		getText: getter(node, "textContent"),
		setText: setter(node, "textContent"),
		localName: getter(element, "localName"),
		getAttribute: method(element, "getAttribute"),
		setAttribute: method(element, "setAttribute"),
		removeAttribute: method(element, "removeAttribute"),
		rect: method(element, "getBoundingClientRect"),
		attachShadow: method(element, "attachShadow"),
		style: getter(w.HTMLElement.prototype, "style"),
		setProperty: method(css, "setProperty"),
		getPropertyValue: method(css, "getPropertyValue"),
		preventDefault: method(event, "preventDefault"),
		stopImmediatePropagation: method(event, "stopImmediatePropagation"),
		target: getter(event, "target"),
		clientX: getter(mouse, "clientX"),
		clientY: getter(mouse, "clientY"),
		relatedTarget: getter(mouse, "relatedTarget"),
	};
}
