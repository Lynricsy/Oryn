import { describe, expect, it } from "vitest";
import { parseConfigList } from "./config-list";

describe("parseConfigList", () => {
	it("/mcp list", () => {
		expect(
			parseConfigList(
				"github | http | enabled | https://api.x.com/mcp [user]\nfs | stdio | disabled | npx -y @a/b [project]",
			),
		).toEqual({
			kind: "mcp",
			rows: [
				{
					cells: ["github", "http", "enabled", "https://api.x.com/mcp"],
					scope: "user",
				},
				{ cells: ["fs", "stdio", "disabled", "npx -y @a/b"], scope: "project" },
			],
		});
	});

	it("/ssh list", () => {
		expect(parseConfigList("box | 10.0.0.2 | - | 22 [project]")).toEqual({
			kind: "ssh",
			rows: [{ cells: ["box", "10.0.0.2", "-", "22"], scope: "project" }],
		});
	});

	it("有一行不匹配就不算（空列表的提示句、普通文本）", () => {
		expect(parseConfigList("No MCP servers configured.")).toBeNull();
		expect(
			parseConfigList("github | http | enabled | https://x [user]\nextra"),
		).toBeNull();
	});
});
