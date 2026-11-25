import { describe, expect, it } from "vitest";
import {
	buildAskFormData,
	createInitialFormValues,
	normalizeRpcFormSections,
	parseAskHtml,
	resolveAskFormDoc,
	resolveFormImageSource,
	sanitizeSvgMarkup,
	splitOptionLabel,
	validateAskForm,
} from "./ask-form";

describe("ask-form：HTML 解析与提交语义", () => {
	const SAMPLE_HTML = `
<form>
  <p>Sessions live in memory and are lost on restart. See <a href="https://example.com/docs">docs</a>.</p>
  <img src="screenshots/home.png" alt="首页布局草图" width="320">
  <svg viewBox="0 0 120 40" aria-label="架构简图">
    <rect x="2" y="2" width="116" height="36" fill="var(--surface)" stroke="var(--accent)" />
    <text x="60" y="24" text-anchor="middle" fill="var(--fg)">API</text>
  </svg>
  <fieldset>
    <legend>Session store</legend>
    <label><input type="radio" name="store" value="sqlite" checked> SQLite: one file, no new service</label>
    <label><input type="radio" name="store" value="redis"> Redis: shared across instances, needs ops</label>
  </fieldset>
  <fieldset>
    <legend>Features</legend>
    <label><input type="checkbox" name="feat" value="a" checked> Auth</label>
    <label><input type="checkbox" name="feat" value="b"> Metrics</label>
  </fieldset>
  <label><input type="checkbox" name="agree" value="yes"> I confirm the migration</label>
  <label>Region
    <select name="region">
      <option value="us">US East</option>
      <option value="eu" selected>EU West</option>
    </select>
  </label>
  <label>Session lifetime (days) <input type="number" name="ttl_days" value="30" min="1" max="365" required></label>
  <label>Callback URL <input type="url" name="hook" placeholder="https://..."></label>
</form>
`;

	it("完整解析 HTML 富文本、图片、SVG、单选/多选/独立复选/select/number 控件", () => {
		const doc = parseAskHtml(SAMPLE_HTML);
		expect(doc).not.toBeNull();
		expect(doc?.source).toBe("html");
		const kinds = doc?.sections.flatMap((s) => s.blocks.map((b) => b.kind));
		expect(kinds).toContain("paragraph");
		expect(kinds).toContain("image");
		expect(kinds).toContain("svg");
		expect(doc?.fields.map((f) => f.name)).toEqual([
			"store",
			"feat",
			"agree",
			"region",
			"ttl_days",
			"hook",
		]);
		const storeField = doc?.fields[0];
		expect(storeField?.kind === "choice" && storeField.other).toBe(true);
		const agreeField = doc?.fields[2];
		expect(agreeField?.kind === "choice" && agreeField.standalone).toBe(true);
		expect(agreeField?.kind === "choice" && agreeField.other).toBe(false);
	});

	it("默认值构造与提交数据类型符合 omp RPC 协议（文本与 number 传字符串、多选与复选传字符串数组）", () => {
		const doc = parseAskHtml(SAMPLE_HTML);
		expect(doc).not.toBeNull();
		if (!doc) return;
		const values = createInitialFormValues(doc);
		expect(validateAskForm(doc, values)).toEqual({});
		expect(buildAskFormData(doc, values)).toEqual({
			store: "sqlite",
			feat: ["a"],
			agree: [],
			region: "eu",
			ttl_days: "30",
			hook: "",
		});

		// 切换到「其他」答案、多选追加自由文本、勾选独立复选框
		values.store = {
			selected: ["sqlite"],
			text: "postgres",
			otherActive: true,
		};
		values.feat = { selected: ["a", "b"], text: "tracing" };
		values.agree = { selected: ["yes"], text: "" };
		expect(buildAskFormData(doc, values)).toEqual({
			store: "postgres",
			feat: ["a", "b", "tracing"],
			agree: ["yes"],
			region: "eu",
			ttl_days: "30",
			hook: "",
		});
	});

	it("校验 required / min / max / url 约束", () => {
		const doc = parseAskHtml(SAMPLE_HTML);
		expect(doc).not.toBeNull();
		if (!doc) return;
		const values = createInitialFormValues(doc);
		values.ttl_days = { text: "0", selected: [] };
		values.hook = { text: "not-a-url", selected: [] };
		const errs = validateAskForm(doc, values);
		expect(errs.ttl_days).toEqual({ key: "min", params: { min: 1 } });
		expect(errs.hook).toEqual({ key: "url" });
	});
});

describe("ask-form：安全清洗（SVG 与图片源）", () => {
	it("SVG 清洗剥离 script、foreignObject、事件属性与外部 href，保留内部 #id", () => {
		const dirty = `<svg width="200" height="100" onload="alert(1)">
			<defs><linearGradient id="g1"><stop offset="0%" stop-color="var(--accent)"/></linearGradient></defs>
			<script>alert(1)</script>
			<foreignObject><div>evil</div></foreignObject>
			<rect x="0" y="0" width="200" height="100" fill="url(#g1)" onclick="hack()" />
			<use href="https://evil.example/a.svg#x" />
			<use href="#g1" />
		</svg>`;
		const clean = sanitizeSvgMarkup(dirty);
		expect(clean).not.toBeNull();
		expect(clean).not.toContain("<script");
		expect(clean).not.toContain("foreignObject");
		expect(clean).not.toContain("onload");
		expect(clean).not.toContain("onclick");
		expect(clean).not.toContain("https://evil.example");
		expect(clean).toContain('viewBox="0 0 200 100"');
		expect(clean).toContain("url(#g1)");
		expect(clean).toContain('href="#g1"');
	});

	it("解析外链、栅格 data URI、工作区相对路径与非法协议", () => {
		expect(resolveFormImageSource("https://cdn.example/a.png")).toEqual({
			kind: "external",
			url: "https://cdn.example/a.png",
		});
		expect(
			resolveFormImageSource("data:image/png;base64,iVBORw0KGgo="),
		).toEqual({
			kind: "data",
			url: "data:image/png;base64,iVBORw0KGgo=",
		});
		expect(resolveFormImageSource("shots/ui.png", "/home/user/repo")).toEqual({
			kind: "orb",
			path: "/home/user/repo/shots/ui.png",
		});
		expect(resolveFormImageSource("javascript:alert(1)")).toEqual({
			kind: "invalid",
		});
	});
});

describe("ask-form：选项拆行与 liveTools 匹配", () => {
	it("拆分「标题: 说明」与「标题 — 说明」，不误拆 URL", () => {
		expect(splitOptionLabel("SQLite: one file, no new service")).toEqual({
			title: "SQLite",
			description: "one file, no new service",
		});
		expect(splitOptionLabel("保留现状 — 不改配置")).toEqual({
			title: "保留现状",
			description: "不改配置",
		});
		expect(splitOptionLabel("https://example.com/a")).toEqual({
			title: "https://example.com/a",
			description: null,
		});
	});

	it("从运行中的 ask liveTool 匹配原始 HTML，无匹配时降级 RPC sections", () => {
		const rpcSections = [
			{
				title: [{ text: "Choose DB" }],
				blocks: [
					{
						kind: "field" as const,
						field: {
							kind: "choice" as const,
							name: "db",
							multi: false,
							select: false,
							other: true,
							label: [],
							required: true,
							options: [
								{
									value: "pg",
									label: [{ text: "Postgres" }],
									checked: true,
									disabled: false,
								},
							],
						},
					},
				],
			},
		];
		const matched = resolveAskFormDoc(
			rpcSections,
			{
				tc_1: {
					toolCallId: "tc_1",
					toolName: "ask",
					args: {
						html: `<form><img src="db.png" alt="DB"><fieldset><legend>Choose DB</legend><label><input type="radio" name="db" value="pg" checked> Postgres: battle-tested</label></fieldset></form>`,
					},
					ended: false,
					startedAt: 1,
				},
			},
			["tc_1"],
			[],
		);
		expect(matched.source).toBe("html");
		expect(
			matched.sections.some((s) => s.blocks.some((b) => b.kind === "image")),
		).toBe(true);

		const fallback = normalizeRpcFormSections(rpcSections);
		expect(fallback.source).toBe("rpc");
		expect(fallback.fields[0]?.name).toBe("db");
	});
});
