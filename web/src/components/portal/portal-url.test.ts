import { describe, expect, it } from "vitest";
import {
	firstLinkPath,
	formatRemaining,
	normalizePath,
	portalFrameSrc,
	portalHost,
	portalUrl,
} from "./portal-url";

const PROD = "https://t-3f9a1c0b7e2d-p{port}.orb.example.com/";
const DEV = "http://t-3f9a1c0b7e2d-p{port}.localhost:7600/";
const MIN = 60_000;

describe("portal-url", () => {
	it("公开地址：代入端口、拼路径（补前导 /，保留查询）", () => {
		expect(portalUrl(PROD, 5173, "/")).toBe(
			"https://t-3f9a1c0b7e2d-p5173.orb.example.com/",
		);
		expect(portalUrl(PROD, 5173, "/docs?x=1&y=%20")).toBe(
			"https://t-3f9a1c0b7e2d-p5173.orb.example.com/docs?x=1&y=%20",
		);
		expect(portalUrl(PROD, 8080, "api")).toBe(
			"https://t-3f9a1c0b7e2d-p8080.orb.example.com/api",
		);
		// 非默认端口原样带上
		expect(portalUrl(DEV, 8000, "/a")).toBe(
			"http://t-3f9a1c0b7e2d-p8000.localhost:7600/a",
		);
	});

	it("iframe 地址先到 /__oryn/login，路径整体编码进 next", () => {
		expect(portalFrameSrc(PROD, 5173, "/docs?x=1&y=2")).toBe(
			"https://t-3f9a1c0b7e2d-p5173.orb.example.com/__oryn/login?next=%2Fdocs%3Fx%3D1%26y%3D2",
		);
		expect(portalFrameSrc(DEV, 8000, "")).toBe(
			"http://t-3f9a1c0b7e2d-p8000.localhost:7600/__oryn/login?next=%2F",
		);
	});

	it("模板不是 http(s)（mock 的 data: 页面）时只代入端口，不拼路径", () => {
		const data = "data:text/html,<p>{port}</p>";
		expect(portalFrameSrc(data, 5173, "/docs")).toBe(
			"data:text/html,<p>5173</p>",
		);
		expect(portalUrl(data, 5173, "/docs")).toBe("data:text/html,<p>5173</p>");
		expect(portalHost(data, 5173)).toBe(":5173");
	});

	it("胶囊主机名含非默认端口", () => {
		expect(portalHost(PROD, 5173)).toBe("t-3f9a1c0b7e2d-p5173.orb.example.com");
		expect(portalHost(DEV, 8000)).toBe("t-3f9a1c0b7e2d-p8000.localhost:7600");
	});

	it("路径规范化与首个链接", () => {
		expect(normalizePath("  ")).toBe("/");
		expect(normalizePath(" docs ")).toBe("/docs");
		expect(firstLinkPath({ links: [] })).toBe("/");
		expect(
			firstLinkPath({
				links: [
					{ title: "Docs", path: "docs" },
					{ title: "web", path: "/" },
				],
			}),
		).toBe("/docs");
	});

	it("剩余时间取最大单位：分钟 → 小时 → 天，不足 1 分钟按 1 分钟", () => {
		const fmt = (n: number, unit: string) =>
			new Intl.NumberFormat("en", {
				style: "unit",
				unit,
				unitDisplay: "long",
			}).format(n);
		expect(formatRemaining(20_000, "en")).toBe(fmt(1, "minute"));
		expect(formatRemaining(59 * MIN + 1, "en")).toBe(fmt(1, "hour"));
		expect(formatRemaining(45 * MIN, "en")).toBe(fmt(45, "minute"));
		expect(formatRemaining(3 * 60 * MIN, "en")).toBe(fmt(3, "hour"));
		expect(formatRemaining(24 * 60 * MIN - 2 * MIN, "en")).toBe(
			fmt(23, "hour"),
		);
		expect(formatRemaining(7 * 24 * 60 * MIN, "en")).toBe(fmt(7, "day"));
	});
});
