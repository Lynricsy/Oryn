import { gzipSync, strToU8, zipSync } from "fflate";
import { describe, expect, it } from "vitest";
import {
	listArchive,
	listTar,
	listTgz,
	listZip,
	MAX_ARCHIVE_ENTRIES,
} from "./archive-list";

/** 手工拼一个 512 字节 tar 头：八进制字段 + 校验和（校验和字段先按空格计算）。 */
function tarHeader(
	name: string,
	size: number,
	opts: {
		type?: string;
		mtime?: number;
		prefix?: string;
		gnu?: boolean;
	} = {},
): Uint8Array {
	const h = new Uint8Array(512);
	const put = (off: number, s: string) => h.set(strToU8(s), off);
	const octal = (off: number, len: number, v: number) =>
		put(off, `${v.toString(8).padStart(len - 1, "0")}\0`);
	put(0, name);
	octal(100, 8, 0o644);
	octal(108, 8, 0);
	octal(116, 8, 0);
	octal(124, 12, size);
	octal(136, 12, opts.mtime ?? 0);
	put(156, opts.type ?? "0");
	if (opts.gnu) put(257, "ustar  \0");
	else {
		put(257, "ustar\0");
		put(263, "00");
	}
	if (opts.prefix) put(345, opts.prefix);
	h.fill(0x20, 148, 156);
	let sum = 0;
	for (const b of h) sum += b;
	put(148, `${sum.toString(8).padStart(6, "0")}\0 `);
	return h;
}

/** 内容补齐到 512 字节边界。 */
function tarData(data: Uint8Array): Uint8Array {
	const out = new Uint8Array(Math.ceil(data.length / 512) * 512);
	out.set(data);
	return out;
}

function concat(...parts: Uint8Array[]): Uint8Array {
	const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
	let off = 0;
	for (const p of parts) {
		out.set(p, off);
		off += p.length;
	}
	return out;
}

function tarFile(
	name: string,
	content: string,
	opts: Parameters<typeof tarHeader>[2] = {},
): Uint8Array {
	const data = strToU8(content);
	return concat(tarHeader(name, data.length, opts), tarData(data));
}

const END = new Uint8Array(1024);

function paxRecord(key: string, value: string): string {
	const body = ` ${key}=${value}\n`;
	// 记录长度包含长度数字本身
	let len = body.length + 1;
	while (String(len).length + strToU8(body).length !== len) len++;
	return `${len}${body}`;
}

describe("listZip", () => {
	it("读出中央目录里的路径、尺寸、目录标记与修改时间", () => {
		const mtime = new Date(2020, 0, 2, 3, 4, 6);
		const zip = zipSync({
			"a.txt": [strToU8("hello"), { mtime }],
			"dir/": new Uint8Array(0),
			"dir/中文.txt": [strToU8("x".repeat(1000)), { level: 9, mtime }],
		});
		const r = listZip(zip);
		expect(r.truncated).toBe(false);
		expect(r.entries).toEqual([
			{ path: "a.txt", size: 5, dir: false, mtime: mtime.getTime() },
			expect.objectContaining({ path: "dir/", size: 0, dir: true }),
			{ path: "dir/中文.txt", size: 1000, dir: false, mtime: mtime.getTime() },
		]);
	});

	it("ZIP64 扩展字段给出的原始尺寸", () => {
		// 手工拼一个仅含中央目录的归档：主记录尺寸为 0xFFFFFFFF，真实值在 0x0001 扩展字段里
		const name = strToU8("big.bin");
		const extra = new Uint8Array(12);
		const ev = new DataView(extra.buffer);
		ev.setUint16(0, 0x0001, true);
		ev.setUint16(2, 8, true);
		ev.setUint32(4, 0x2345_6789, true);
		ev.setUint32(8, 0x1, true); // 高 32 位 → 0x1_2345_6789
		const cd = new Uint8Array(46 + name.length + extra.length);
		const cv = new DataView(cd.buffer);
		cv.setUint32(0, 0x02014b50, true);
		cv.setUint32(20, 0xffffffff, true);
		cv.setUint32(24, 0xffffffff, true);
		cv.setUint16(28, name.length, true);
		cv.setUint16(30, extra.length, true);
		cd.set(name, 46);
		cd.set(extra, 46 + name.length);
		const eocd = new Uint8Array(22);
		const dv = new DataView(eocd.buffer);
		dv.setUint32(0, 0x06054b50, true);
		dv.setUint16(8, 1, true);
		dv.setUint16(10, 1, true);
		dv.setUint32(12, cd.length, true);
		dv.setUint32(16, 0, true);
		const r = listZip(concat(cd, eocd));
		expect(r.entries).toEqual([
			{ path: "big.bin", size: 0x1_2345_6789, dir: false },
		]);
	});

	it("前面拼了其他数据（自解压）时仍能定位中央目录", () => {
		const zip = zipSync({ "a.txt": strToU8("hi") });
		const r = listZip(concat(new Uint8Array(100).fill(7), zip));
		expect(r.entries.map((e) => e.path)).toEqual(["a.txt"]);
	});

	it(`超过 ${MAX_ARCHIVE_ENTRIES} 条时截断`, () => {
		const files: Record<string, Uint8Array> = {};
		for (let i = 0; i <= MAX_ARCHIVE_ENTRIES; i++) {
			files[`f${i}`] = new Uint8Array(0);
		}
		const r = listZip(zipSync(files, { level: 0 }));
		expect(r.truncated).toBe(true);
		expect(r.entries).toHaveLength(MAX_ARCHIVE_ENTRIES);
	});

	it("损坏的输入抛错", () => {
		expect(() => listZip(new Uint8Array([1, 2, 3]))).toThrow();
		expect(() => listZip(new Uint8Array(200).fill(9))).toThrow();
		const zip = zipSync({ "a.txt": strToU8("hello") });
		// 截掉中央目录的前半段
		const cut = zip.slice(0, 20);
		expect(() => listZip(concat(cut, zip.slice(zip.length - 22)))).toThrow();
	});
});

describe("listTar", () => {
	it("ustar 头：尺寸、目录、修改时间与 prefix 字段", () => {
		const tar = concat(
			tarHeader("./", 0, { type: "5" }),
			tarHeader("src/", 0, { type: "5", mtime: 1_600_000_000 }),
			tarFile("src/main.ts", "console.log(1)\n", { mtime: 1_600_000_001 }),
			tarFile("file.txt", "abc", { prefix: "very/long/prefix" }),
			END,
		);
		const r = listTar(tar);
		expect(r.truncated).toBe(false);
		expect(r.entries).toEqual([
			{ path: "src/", size: 0, dir: true, mtime: 1_600_000_000_000 },
			{ path: "src/main.ts", size: 15, dir: false, mtime: 1_600_000_001_000 },
			{ path: "very/long/prefix/file.txt", size: 3, dir: false },
		]);
	});

	it("GNU 长文件名（L）与 pax 扩展头（x）", () => {
		const long = `${"d/".repeat(80)}long.txt`;
		const longData = strToU8(`${long}\0`);
		const pax = strToU8(
			paxRecord("path", "pax/名字.txt") + paxRecord("size", "4"),
		);
		const tar = concat(
			tarHeader("././@LongLink", longData.length, { type: "L", gnu: true }),
			tarData(longData),
			tarHeader("truncated-name", 2, { gnu: true }),
			tarData(strToU8("hi")),
			tarHeader("PaxHeaders/x", pax.length, { type: "x" }),
			tarData(pax),
			tarHeader("short", 0),
			tarData(strToU8("abcd")),
			tarFile("after.txt", "z"),
			END,
		);
		expect(listTar(tar).entries).toEqual([
			{ path: long, size: 2, dir: false },
			{ path: "pax/名字.txt", size: 4, dir: false },
			{ path: "after.txt", size: 1, dir: false },
		]);
	});

	it(`超过 ${MAX_ARCHIVE_ENTRIES} 条时截断`, () => {
		const parts: Uint8Array[] = [];
		for (let i = 0; i <= MAX_ARCHIVE_ENTRIES; i++) {
			parts.push(tarHeader(`f${i}`, 0));
		}
		parts.push(END);
		const r = listTar(concat(...parts));
		expect(r.truncated).toBe(true);
		expect(r.entries).toHaveLength(MAX_ARCHIVE_ENTRIES);
	});

	it("空归档为空列表，损坏的头部抛错", () => {
		expect(listTar(new Uint8Array(0)).entries).toEqual([]);
		expect(listTar(END).entries).toEqual([]);
		expect(() => listTar(new Uint8Array(1024).fill(0x41))).toThrow();
		const bad = tarFile("a.txt", "abc");
		bad[0] = 0x7a; // 改名不改校验和
		expect(() => listTar(concat(bad, END))).toThrow();
	});
});

describe("listArchive", () => {
	const tar = concat(tarFile("a.txt", "hello"), END);

	it("按扩展名分派到 zip / tar / tgz", () => {
		const zip = zipSync({ "x.py": strToU8("print()") });
		expect(listArchive("pkg.whl", zip).entries[0]?.path).toBe("x.py");
		expect(listArchive("APP.JAR", zip).entries[0]?.path).toBe("x.py");
		expect(listArchive("a.tar", tar).entries[0]?.path).toBe("a.txt");
		expect(listArchive("a.tar.gz", gzipSync(tar)).entries).toEqual([
			{ path: "a.txt", size: 5, dir: false },
		]);
		expect(listArchive("a.tgz", gzipSync(tar)).entries).toHaveLength(1);
	});

	it("不支持的类型与损坏的 gzip 抛错", () => {
		expect(() => listArchive("a.rar", tar)).toThrow();
		expect(() => listArchive("a.tgz", tar)).toThrow();
	});

	it("tgz 流式解压：头部、pax 与长文件名跨解压块边界时结果与整份 tar 一致", () => {
		const parts: Uint8Array[] = [];
		for (let i = 0; i < 300; i++) {
			const long = `${"d".repeat(120)}/file-${i}.txt`;
			const pax = strToU8(paxRecord("path", long));
			parts.push(
				tarHeader("PaxHeader", pax.length, { type: "x" }),
				tarData(pax),
				tarFile(`short-${i}`, "x".repeat(i)),
			);
		}
		const tar = concat(...parts, END);
		// level 0 = 存储块，压缩后体积与原文相当，保证解压输出被切成很多块
		const tgz = gzipSync(tar, { level: 0 });
		expect(tgz.length).toBeGreaterThan(100 * 1024);
		expect(listArchive("a.tgz", tgz)).toEqual(listTar(tar));
	});

	it("tgz 解压量超过上限时停止解压，只列出此前的条目并标记 partial", () => {
		// 高压缩比的包：8MiB 的零展开自几 KiB 的压缩数据
		const big = 8 << 20;
		const tgz = gzipSync(
			concat(
				tarHeader("big.bin", big),
				new Uint8Array(big),
				tarFile("after.txt", "x"),
				END,
			),
		);
		expect(tgz.length).toBeLessThan(64 * 1024);

		const capped = listTgz(tgz, 1 << 20);
		expect(capped.partial).toBe(true);
		expect(capped.entries.map((e) => e.path)).toEqual(["big.bin"]);

		const full = listTgz(tgz, 16 << 20);
		expect(full.partial).toBeUndefined();
		expect(full.entries.map((e) => e.path)).toEqual(["big.bin", "after.txt"]);
	});
});
