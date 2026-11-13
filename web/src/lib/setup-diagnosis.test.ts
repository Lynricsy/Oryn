import { describe, expect, it } from "vitest";
import { diagnoseSetupRun } from "./setup-diagnosis";

const failed = (error?: string, phase: "setup" | "resume" = "setup") => ({
	ok: false,
	phase,
	error,
});

describe("diagnoseSetupRun", () => {
	it("成功的运行没有诊断", () => {
		expect(diagnoseSetupRun({ ok: true, phase: "setup" })).toBeNull();
	});

	it("超时按阶段给出上限", () => {
		expect(
			diagnoseSetupRun(
				failed("context deadline exceeded (signal: killed)", "resume"),
			),
		).toEqual({ kind: "timeout", limitMin: 10 });
		expect(
			diagnoseSetupRun(failed("context deadline exceeded (signal: killed)")),
		).toEqual({ kind: "timeout", limitMin: 20 });
	});

	it("orb 停止或重启时被取消", () => {
		expect(
			diagnoseSetupRun(failed("context canceled (signal: killed)")),
		).toEqual({ kind: "cancelled" });
	});

	it("被系统杀掉（signal killed / 137）判为内存不足，并认出是哪个脚本", () => {
		expect(diagnoseSetupRun(failed(".oryn/setup: signal: killed"))).toEqual({
			kind: "oom",
			script: ".oryn/setup",
		});
		expect(
			diagnoseSetupRun(failed("project setup script: exit status 137")),
		).toEqual({ kind: "oom", script: null });
	});

	it("127 从日志尾部取出找不到的命令名", () => {
		const log =
			"running .oryn/setup\n+ pnpm install\n.oryn/setup: line 4: pnpm: command not found\n";
		expect(
			diagnoseSetupRun(failed(".oryn/setup: exit status 127"), log),
		).toEqual({ kind: "notFound", script: ".oryn/setup", command: "pnpm" });
	});

	it("日志尚未到达时，127 仍能判断为命令不存在", () => {
		expect(diagnoseSetupRun(failed(".oryn/setup: exit status 127"))).toEqual({
			kind: "notFound",
			script: ".oryn/setup",
			command: undefined,
		});
	});

	it("普通退出码靠日志尾部识别网络、磁盘、权限", () => {
		const err = ".oryn/setup: exit status 1";
		expect(
			diagnoseSetupRun(
				failed(err),
				"npm ERR! code ENOTFOUND\nnpm ERR! request to https://registry.npmjs.org failed\n",
			)?.kind,
		).toBe("network");
		expect(
			diagnoseSetupRun(
				failed(err),
				"cp: error writing 'x': No space left on device\n",
			)?.kind,
		).toBe("disk");
		expect(
			diagnoseSetupRun(
				failed(err),
				"mkdir: cannot create directory '/opt/x': Permission denied\n",
			)?.kind,
		).toBe("permission");
	});

	it("软件源里找不到的包写出包名（apt / pip / npm）", () => {
		const err = ".oryn/setup: exit status 100";
		expect(
			diagnoseSetupRun(
				failed(err),
				"Reading package lists...\nE: Unable to locate package postgresql-16\n",
			),
		).toEqual({ kind: "package", script: ".oryn/setup", pkg: "postgresql-16" });
		expect(
			diagnoseSetupRun(
				failed(err),
				"ERROR: No matching distribution found for torch==9.9\n",
			)?.pkg,
		).toBe("torch==9.9");
		expect(
			diagnoseSetupRun(
				failed(err),
				"npm error 404 Not Found - GET https://registry.npmjs.org/@acme%2fnope\n",
			)?.pkg,
		).toBe("@acme/nope");
	});

	it("早先已恢复的报错不影响结论：只看日志尾部", () => {
		const early = "Could not resolve host: github.com\n";
		const filler = Array.from({ length: 80 }, (_, i) => `step ${i}`).join("\n");
		expect(
			diagnoseSetupRun(
				failed(".oryn/setup: exit status 2"),
				`${early}${filler}\nmake: *** [all] Error 2\n`,
			),
		).toEqual({ kind: "exit", script: ".oryn/setup", code: 2 });
	});

	it("认不出的错误保留为 unknown", () => {
		expect(diagnoseSetupRun(failed("write /home/u/.npmrc: read-only"))).toEqual(
			{ kind: "unknown" },
		);
		expect(diagnoseSetupRun(failed(undefined))).toEqual({ kind: "unknown" });
	});
});
