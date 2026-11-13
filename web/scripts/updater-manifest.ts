#!/usr/bin/env bun
/**
 * 生成 Tauri updater 的静态清单 `latest.json`（M6 计划 8.2 `publish` job 与验收 D11 使用）。
 *
 * 用法（仓库根目录）：
 *   bun web/scripts/updater-manifest.ts --repo <owner/name> --tag <desktop-vX.Y.Z> --version <X.Y.Z> > latest.json
 * 读 `gh release view --json assets` 的资产列表，`gh release download -p '*.sig'` 取签名内容。
 * 一个 `.sig` 都没有（构建时没配更新私钥）→ 退出码 3，stdout 不输出任何内容。
 *
 * 下载地址按 `https://github.com/<repo>/releases/download/<tag>/<资产名>` 拼出，而不是取 gh 返回的 `url`：
 * 草稿 Release 的资产地址是 `…/download/untagged-…/…`，发布（`--draft=false`）后就失效了。
 */
import { mkdtempSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";

export interface ReleaseAsset {
	name: string;
	url: string;
}

export interface PlatformEntry {
	signature: string;
	url: string;
}

export interface UpdaterManifest {
	version: string;
	notes: string;
	pub_date: string;
	platforms: Record<string, PlatformEntry>;
}

/** 没有任何可用签名时抛出（CLI 映射为退出码 3）。 */
export class NoSignaturesError extends Error {
	constructor() {
		super(
			"没有任何带 .sig 签名的更新包（构建时未配置 TAURI_SIGNING_PRIVATE_KEY？）",
		);
		this.name = "NoSignaturesError";
	}
}

/** 同一平台匹配到多个更新包时抛出（清单只能指向一个）。 */
export class AmbiguousAssetError extends Error {
	constructor(platform: string, names: string[]) {
		super(`平台 ${platform} 匹配到多个更新包：${names.join(", ")}`);
		this.name = "AmbiguousAssetError";
	}
}

/** 资产后缀 → 清单平台键。`.app.tar.gz` 是通用二进制（universal-apple-darwin），两种 mac 架构共用。 */
const RULES: { suffix: string; platforms: string[] }[] = [
	{ suffix: ".AppImage", platforms: ["linux-x86_64"] },
	{ suffix: "-setup.exe", platforms: ["windows-x86_64"] },
	{ suffix: ".app.tar.gz", platforms: ["darwin-aarch64", "darwin-x86_64"] },
];

/**
 * 按后缀挑出各平台的更新包，配上同名 `.sig` 的内容。
 * @param assets Release 资产（含 `.sig` 本身也无妨，会被忽略）
 * @param sigs `<资产名>.sig` → 签名文件内容
 * 缺签名的平台不进清单；所有平台都没有签名 → NoSignaturesError。
 */
export function buildManifest(
	assets: ReleaseAsset[],
	sigs: Record<string, string>,
	version: string,
	pubDate: string,
): UpdaterManifest {
	const platforms: Record<string, PlatformEntry> = {};
	for (const rule of RULES) {
		const hits = assets.filter((a) => a.name.endsWith(rule.suffix));
		if (hits.length > 1)
			throw new AmbiguousAssetError(
				rule.platforms.join("/"),
				hits.map((a) => a.name),
			);
		const asset = hits[0];
		if (!asset) continue;
		const signature = sigs[`${asset.name}.sig`]?.trim();
		if (!signature) continue;
		for (const p of rule.platforms)
			platforms[p] = { signature, url: asset.url };
	}
	if (Object.keys(platforms).length === 0) throw new NoSignaturesError();
	return { version, notes: "", pub_date: pubDate, platforms };
}

// ---------------------------------------------------------------- 命令行

function arg(argv: string[], name: string): string {
	const i = argv.indexOf(name);
	const v = i >= 0 ? argv[i + 1] : undefined;
	if (!v) {
		console.error(
			"用法：updater-manifest.ts --repo <owner/name> --tag <tag> --version <version>",
		);
		process.exit(2);
	}
	return v;
}

function gh(args: string[]): string {
	const r = Bun.spawnSync(["gh", ...args], { stderr: "pipe" });
	if (r.exitCode !== 0)
		throw new Error(`gh ${args.join(" ")} 失败：${r.stderr.toString().trim()}`);
	return r.stdout.toString();
}

function main(argv: string[]) {
	const repo = arg(argv, "--repo");
	const tag = arg(argv, "--tag");
	const version = arg(argv, "--version");
	const view = JSON.parse(
		gh(["release", "view", tag, "--repo", repo, "--json", "assets"]),
	) as { assets: { name: string }[] };
	const assets: ReleaseAsset[] = view.assets.map((a) => ({
		name: a.name,
		url: `https://github.com/${repo}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(a.name)}`,
	}));
	const sigs: Record<string, string> = {};
	if (assets.some((a) => a.name.endsWith(".sig"))) {
		const dir = mkdtempSync(`${tmpdir()}/oryn-sigs-`);
		try {
			gh([
				"release",
				"download",
				tag,
				"--repo",
				repo,
				"-p",
				"*.sig",
				"-D",
				dir,
			]);
			for (const f of readdirSync(dir))
				sigs[f] = readFileSync(`${dir}/${f}`, "utf8");
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	}
	try {
		const m = buildManifest(assets, sigs, version, new Date().toISOString());
		process.stdout.write(`${JSON.stringify(m, null, 2)}\n`);
	} catch (e) {
		if (e instanceof NoSignaturesError) {
			console.error(e.message);
			process.exit(3);
		}
		throw e;
	}
}

if (import.meta.main) main(process.argv.slice(2));
