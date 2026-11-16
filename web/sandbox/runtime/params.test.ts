import { JSDOM } from "jsdom";
import { afterEach, describe, expect, it, vi } from "vitest";
import vectors from "../../../schema/testdata/artifact-params.json";
import { install } from "./start";

const CHANNEL = "0123456789abcdef";

function pageWith(decl: unknown, attrs = 'type="application/json"'): string {
	const json = JSON.stringify(decl).replaceAll("</", "<\\/");
	return `<!doctype html><html><head><script ${attrs} data-oryn-params>${json}</script></head><body><h1>x</h1></body></html>`;
}

function setup(html: string, runtime = true) {
	const parent = new JSDOM("", { url: "https://app.example/" }).window;
	const page = new JSDOM(html, { url: "https://a-token.example/" }).window;
	const posted = vi.fn();
	vi.spyOn(parent, "postMessage").mockImplementation(posted);
	install(page, parent, {
		v: 1,
		mark: "ab12cd34",
		path: "index.html",
		runtime,
	});
	const deliver = (data: unknown) =>
		page.dispatchEvent(
			new page.MessageEvent("message", { data, source: parent }),
		);
	const root = page.document.documentElement;
	return { posted, deliver, root };
}

afterEach(() => {
	vi.restoreAllMocks();
});

const DECL = {
	accent: { type: "color", value: "#2563eb", label: "强调色" },
	size: { type: "number", value: 16, min: 8, max: 32, unit: "px" },
	dark: { type: "boolean", value: false },
	Bad: { type: "color", value: "#000" },
};

describe("页面运行时的调节参数", () => {
	// 与 Go 的 ParamsPrelude 读同一份向量：两边设到根元素上的 CSS 变量与 data 属性相同
	for (const c of vectors.decls) {
		if (!("css" in c) || !c.css) continue;
		it(`声明设到根元素上：${c.name}`, () => {
			const { root } = setup(pageWith(c.decl));
			for (const [name, css] of Object.entries(c.css)) {
				expect(root.style.getPropertyValue(`--${name}`)).toBe(css.variable);
				expect(root.getAttribute(`data-${name}`)).toBe(css.attribute);
			}
		});
	}

	it("ready 带规整后的声明；params 即时套用，未声明与不合规的略过", () => {
		const { posted, deliver, root } = setup(pageWith(DECL));
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		const ready = posted.mock.calls[0]?.[0];
		expect(ready.kind).toBe("ready");
		expect(Object.keys(ready.params)).toEqual(["accent", "size", "dark"]);
		expect(ready.params.size).toEqual({
			label: "",
			type: "number",
			value: 16,
			min: 8,
			max: 32,
			step: 1,
			unit: "px",
		});

		deliver({
			v: 1,
			channel: CHANNEL,
			kind: "params",
			values: { accent: "#ff0000", size: 99, dark: "yes", other: true },
		});
		expect(root.style.getPropertyValue("--accent")).toBe("#ff0000");
		expect(root.getAttribute("data-accent")).toBe("#ff0000");
		expect(root.style.getPropertyValue("--size")).toBe("32px");
		expect(root.getAttribute("data-dark")).toBe("false");
		expect(root.hasAttribute("data-other")).toBe(false);
	});

	it("旧版本的帧设声明的值，但不接受预览", () => {
		const { deliver, root } = setup(pageWith(DECL), false);
		expect(root.style.getPropertyValue("--accent")).toBe("#2563eb");
		deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		deliver({
			v: 1,
			channel: CHANNEL,
			kind: "params",
			values: { accent: "#ff0000" },
		});
		expect(root.style.getPropertyValue("--accent")).toBe("#2563eb");
	});

	it("声明不是 JSON 或 type 不对时没有参数，页面照常", () => {
		const bad = setup(
			'<!doctype html><html><head><script type="application/json" data-oryn-params>{oops</script></head></html>',
		);
		bad.deliver({ v: 1, channel: CHANNEL, kind: "hello" });
		expect(bad.posted.mock.calls[0]?.[0].params).toEqual({});
		const wrongType = setup(pageWith(DECL, ""));
		expect(wrongType.root.hasAttribute("data-accent")).toBe(false);
	});
});
