import { describe, expect, it } from "vitest";
import { shellArg } from "./shell-quote";

describe("shellArg", () => {
	it.each([
		["notes", "notes"],
		["a/b-c_d.e", "a/b-c_d.e"],
		["my notes", "'my notes'"],
		["it's", "'it'\\''s'"],
		["", "''"],
		["$HOME", "'$HOME'"],
	])("%s → %s", (input, out) => {
		expect(shellArg(input)).toBe(out);
	});
});
