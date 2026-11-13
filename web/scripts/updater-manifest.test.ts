// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
	AmbiguousAssetError,
	buildManifest,
	NoSignaturesError,
	type ReleaseAsset,
} from "./updater-manifest.ts";

const BASE =
	"https://github.com/Lynricsy/Oryn/releases/download/desktop-v0.1.0";
const asset = (name: string): ReleaseAsset => ({
	name,
	url: `${BASE}/${name}`,
});

const ALL = [
	asset("Oryn_0.1.0_amd64.AppImage"),
	asset("Oryn_0.1.0_amd64.AppImage.sig"),
	asset("Oryn_0.1.0_amd64.deb"),
	asset("Oryn-0.1.0-1.x86_64.rpm"),
	asset("Oryn_0.1.0_x64-setup.exe"),
	asset("Oryn_0.1.0_x64-setup.exe.sig"),
	asset("Oryn_0.1.0_x64_en-US.msi"),
	asset("Oryn_0.1.0_x64_en-US.msi.sig"),
	asset("Oryn_0.1.0_universal.dmg"),
	asset("Oryn_universal.app.tar.gz"),
	asset("Oryn_universal.app.tar.gz.sig"),
];
const SIGS = {
	"Oryn_0.1.0_amd64.AppImage.sig": "sig-linux\n",
	"Oryn_0.1.0_x64-setup.exe.sig": "sig-win",
	"Oryn_0.1.0_x64_en-US.msi.sig": "sig-msi",
	"Oryn_universal.app.tar.gz.sig": "sig-mac",
};
const DATE = "2026-11-13T00:00:00.000Z";

describe("buildManifest", () => {
	it("三平台齐全：四个平台键，mac 两种架构共用通用包", () => {
		const m = buildManifest(ALL, SIGS, "0.1.0", DATE);
		expect(m).toEqual({
			version: "0.1.0",
			notes: "",
			pub_date: DATE,
			platforms: {
				"linux-x86_64": {
					signature: "sig-linux",
					url: `${BASE}/Oryn_0.1.0_amd64.AppImage`,
				},
				"windows-x86_64": {
					signature: "sig-win",
					url: `${BASE}/Oryn_0.1.0_x64-setup.exe`,
				},
				"darwin-aarch64": {
					signature: "sig-mac",
					url: `${BASE}/Oryn_universal.app.tar.gz`,
				},
				"darwin-x86_64": {
					signature: "sig-mac",
					url: `${BASE}/Oryn_universal.app.tar.gz`,
				},
			},
		});
	});

	it("缺某平台的包或签名时不产生该键", () => {
		const noWin = ALL.filter((a) => !a.name.includes("setup.exe"));
		const m1 = buildManifest(noWin, SIGS, "0.1.0", DATE);
		expect(Object.keys(m1.platforms).sort()).toEqual([
			"darwin-aarch64",
			"darwin-x86_64",
			"linux-x86_64",
		]);

		const { "Oryn_universal.app.tar.gz.sig": _mac, ...noMacSig } = SIGS;
		const m2 = buildManifest(ALL, noMacSig, "0.1.0", DATE);
		expect(Object.keys(m2.platforms).sort()).toEqual([
			"linux-x86_64",
			"windows-x86_64",
		]);
	});

	it("没有任何签名 → NoSignaturesError", () => {
		expect(() => buildManifest(ALL, {}, "0.1.0", DATE)).toThrow(
			NoSignaturesError,
		);
		expect(() =>
			buildManifest(
				ALL.filter((a) => !a.name.endsWith(".sig")),
				{ "unrelated.sig": "x" },
				"0.1.0",
				DATE,
			),
		).toThrow(NoSignaturesError);
	});

	it("同一平台多个包 → AmbiguousAssetError", () => {
		expect(() =>
			buildManifest(
				[...ALL, asset("Oryn_0.0.9_amd64.AppImage")],
				SIGS,
				"0.1.0",
				DATE,
			),
		).toThrow(AmbiguousAssetError);
	});
});
