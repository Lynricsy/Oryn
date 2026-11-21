import { describe, expect, it } from "vitest";
import { isGitHubRepo, repoLabel } from "./repo-label";

describe("repoLabel", () => {
	it.each([
		["https://github.com/acme/w.git", "github.com/acme/w"],
		["https://github.com/acme/w", "github.com/acme/w"],
		["https://gitlab.example.com:8443/g/r.git/", "gitlab.example.com:8443/g/r"],
		["git@github.com:acme/w.git", "git@github.com:acme/w.git"],
		["github.com:acme/w", "github.com:acme/w"],
		["not a url", "not a url"],
	])("%s → %s", (url, want) => {
		expect(repoLabel(url)).toBe(want);
	});
});

describe("isGitHubRepo", () => {
	it("只认 github.com 主机", () => {
		expect(isGitHubRepo("https://github.com/acme/w")).toBe(true);
		expect(isGitHubRepo("https://GitHub.com/acme/w")).toBe(true);
		expect(isGitHubRepo("https://gitlab.com/acme/w")).toBe(false);
		expect(isGitHubRepo("https://github.com.evil.io/a/b")).toBe(false);
		expect(isGitHubRepo("git@github.com:acme/w")).toBe(false);
		expect(isGitHubRepo(undefined)).toBe(false);
	});
});
