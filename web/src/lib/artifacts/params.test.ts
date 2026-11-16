import vectors from "../../../../schema/testdata/artifact-params.json";
import {
	coerceParamValue,
	MAX_PARAMS,
	type PageParam,
	paramCss,
	paramsDecl,
	parseParamsDecl,
	parseParamValues,
} from "./params";

interface ValueVector {
	name: string;
	param: unknown;
	value?: unknown;
	repeat?: { text: string; count: number };
	want?: unknown;
	wantSame?: boolean;
	reject?: boolean;
}

// 与 Go 的 params_test.go 读同一份向量：声明的规整、值的校验与 CSS 形式两边逐条相同
describe("调节参数的共享向量", () => {
	for (const c of vectors.decls) {
		it(`声明：${c.name}`, () => {
			const got = parseParamsDecl(c.decl);
			expect(got).toEqual(c.want);
			if ("css" in c && c.css) {
				const css = Object.fromEntries(
					got.map((p) => [p.name, paramCss(p, p.value)]),
				);
				expect(css).toEqual(c.css);
			}
		});
	}
	for (const c of vectors.values as ValueVector[]) {
		it(`值：${c.name}`, () => {
			const [p] = parseParamsDecl({ p: c.param });
			expect(p).toBeDefined();
			const value = c.repeat ? c.repeat.text.repeat(c.repeat.count) : c.value;
			const got = coerceParamValue(p as PageParam, value);
			if (c.reject) expect(got).toBeNull();
			else expect(got).toEqual(c.wantSame ? value : c.want);
		});
	}
});

describe("parseParamsDecl", () => {
	it(`最多取 ${MAX_PARAMS} 个合规的`, () => {
		const decl: Record<string, unknown> = {
			Bad: { type: "boolean", value: true },
		};
		for (let i = 0; i < MAX_PARAMS + 2; i++)
			decl[`p${i}`] = { type: "boolean", value: true };
		const got = parseParamsDecl(decl);
		expect(got).toHaveLength(MAX_PARAMS);
		expect(got[0]?.name).toBe("p0");
	});

	it("paramsDecl 写回之后再解析结果不变", () => {
		const got = parseParamsDecl(vectors.decls[0]?.decl);
		expect(parseParamsDecl(paramsDecl(got))).toEqual(got);
	});
});

describe("parseParamValues", () => {
	it("只收合法参数名与基本类型的值", () => {
		expect(parseParamValues({ accent: "#fff", n: 2, on: true })).toEqual({
			accent: "#fff",
			n: 2,
			on: true,
		});
		expect(parseParamValues({ Accent: "#fff" })).toBeNull();
		expect(parseParamValues({ a: null })).toBeNull();
		expect(parseParamValues({ a: Number.NaN })).toBeNull();
		expect(parseParamValues({ a: {} })).toBeNull();
		expect(parseParamValues([])).toBeNull();
		const many = Object.fromEntries(
			Array.from({ length: MAX_PARAMS + 1 }, (_, i) => [`p${i}`, 1]),
		);
		expect(parseParamValues(many)).toBeNull();
	});
});
