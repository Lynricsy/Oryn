/**
 * 归档目录列举（纯函数，不解压条目数据）：
 * - ZIP 系（zip/jar/apk/whl…）：从文件尾的 End-of-central-directory 定位中央目录，逐条读取；支持 ZIP64 尺寸与偏移、
 *   Info-ZIP Unicode 路径（0x7075）与扩展时间戳（0x5455）。
 * - tar：ustar / GNU（`L` 长文件名、base-256 大尺寸）/ pax（`x` 头的 path、size、mtime）。
 * - tgz / tar.gz：先用 fflate 整体解压再按 tar 列举。
 * 条目数封顶 {@link MAX_ARCHIVE_ENTRIES}，超出时 `truncated` 为 true。输入来自沙箱，所有读取都做越界检查，结构损坏时抛错。
 */
import { gunzipSync } from "fflate";

export interface ArchiveEntry {
	/** 归档内路径，统一用 `/` 分隔；目录以 `/` 结尾与否保持原样 */
	path: string;
	/** 解压后字节数（目录为 0） */
	size: number;
	dir: boolean;
	/** 修改时间（毫秒时间戳）；归档未记录或无效时缺省 */
	mtime?: number;
}

export interface ArchiveListing {
	entries: ArchiveEntry[];
	/** 条目数超过上限、只列出了前 {@link MAX_ARCHIVE_ENTRIES} 条 */
	truncated: boolean;
}

export const MAX_ARCHIVE_ENTRIES = 5000;

/** 按 ZIP 容器处理的扩展名（均为 ZIP 格式的打包文件）。 */
const ZIP_EXTS: Record<string, true> = {
	zip: true,
	jar: true,
	war: true,
	ear: true,
	apk: true,
	xpi: true,
	whl: true,
	nupkg: true,
	vsix: true,
	aar: true,
	ipa: true,
};

/** 按文件名分派：ZIP 系 → {@link listZip}；tar → {@link listTar}；tgz / tar.gz → 解压后 {@link listTar}；其余抛错。 */
export function listArchive(name: string, bytes: Uint8Array): ArchiveListing {
	const lower = name.toLowerCase();
	if (lower.endsWith(".tar.gz") || lower.endsWith(".tgz")) {
		return listTar(gunzipSync(bytes));
	}
	if (lower.endsWith(".tar")) return listTar(bytes);
	const dot = lower.lastIndexOf(".");
	if (dot >= 0 && Object.hasOwn(ZIP_EXTS, lower.slice(dot + 1))) {
		return listZip(bytes);
	}
	throw new Error(`Unsupported archive type: ${name}`);
}

// ───────────────────────────── ZIP ─────────────────────────────

const SIG_EOCD = 0x06054b50;
const SIG_ZIP64_LOCATOR = 0x07064b50;
const SIG_ZIP64_EOCD = 0x06064b50;
const SIG_CENTRAL = 0x02014b50;
const EOCD_SIZE = 22;
const CENTRAL_SIZE = 46;
const U16_MAX = 0xffff;
const U32_MAX = 0xffffffff;

function zipError(detail: string): Error {
	return new Error(`Corrupt ZIP archive: ${detail}`);
}

/** 小端读取器：所有读取先做越界检查，越界即视为归档损坏。 */
class Reader {
	readonly bytes: Uint8Array;
	private readonly view: DataView;
	constructor(bytes: Uint8Array) {
		this.bytes = bytes;
		this.view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
	}
	private check(off: number, len: number): void {
		if (off < 0 || off + len > this.bytes.length)
			throw zipError("out of bounds");
	}
	u8(off: number): number {
		this.check(off, 1);
		return this.view.getUint8(off);
	}
	u16(off: number): number {
		this.check(off, 2);
		return this.view.getUint16(off, true);
	}
	u32(off: number): number {
		this.check(off, 4);
		return this.view.getUint32(off, true);
	}
	/** 64 位无符号；超出 2^53 的值在本场景（浏览器内存里的文件）不可能合法，直接判为损坏。 */
	u64(off: number): number {
		const lo = this.u32(off);
		const hi = this.u32(off + 4);
		const v = hi * 0x1_0000_0000 + lo;
		if (!Number.isSafeInteger(v)) throw zipError("64-bit value too large");
		return v;
	}
	slice(off: number, len: number): Uint8Array {
		this.check(off, len);
		return this.bytes.subarray(off, off + len);
	}
}

const utf8Fatal = new TextDecoder("utf-8", { fatal: true });
const utf8Loose = new TextDecoder("utf-8");
/**
 * 未声明 UTF-8 的旧式文件名：规范默认 CP437，但实际多为系统本地编码（中文 Windows 下为 GBK），这里退到 GB18030
 * （兼容 GBK，ASCII 部分与 CP437 一致）；运行环境不支持该编码时退到 latin1。
 */
const legacyDecoder = (() => {
	try {
		return new TextDecoder("gb18030");
	} catch {
		return new TextDecoder("latin1");
	}
})();

function decodeZipName(raw: Uint8Array, utf8Flag: boolean): string {
	if (utf8Flag) return utf8Loose.decode(raw);
	try {
		return utf8Fatal.decode(raw);
	} catch {
		return legacyDecoder.decode(raw);
	}
}

/** MS-DOS 日期时间（本地时间，2 秒精度）→ 毫秒时间戳；全零或字段越界时返回 undefined。 */
function dosDateTime(date: number, time: number): number | undefined {
	if (date === 0) return undefined;
	const day = date & 0x1f;
	const month = (date >> 5) & 0x0f;
	const year = ((date >> 9) & 0x7f) + 1980;
	if (day < 1 || month < 1 || month > 12) return undefined;
	const sec = (time & 0x1f) * 2;
	const min = (time >> 5) & 0x3f;
	const hour = (time >> 11) & 0x1f;
	return new Date(year, month - 1, day, hour, min, sec).getTime();
}

/** 自尾向前查找 EOCD 记录（其后最多跟 65535 字节注释）。 */
function findEocd(r: Reader): number {
	const len = r.bytes.length;
	const min = Math.max(0, len - EOCD_SIZE - U16_MAX);
	for (let off = len - EOCD_SIZE; off >= min; off--) {
		if (r.u32(off) === SIG_EOCD && off + EOCD_SIZE + r.u16(off + 20) <= len) {
			return off;
		}
	}
	throw zipError("end of central directory not found");
}

/** 读取 ZIP 中央目录，不解压任何条目数据。 */
export function listZip(bytes: Uint8Array): ArchiveListing {
	const r = new Reader(bytes);
	if (bytes.length < EOCD_SIZE) throw zipError("file too small");
	const eocd = findEocd(r);
	let total = r.u16(eocd + 10);
	let cdSize = r.u32(eocd + 12);
	let cdOffset = r.u32(eocd + 16);

	// ZIP64：EOCD 中有字段溢出时，读取紧邻其前的 ZIP64 定位记录指向的 ZIP64 EOCD
	if (total === U16_MAX || cdSize === U32_MAX || cdOffset === U32_MAX) {
		const loc = eocd - 20;
		if (loc >= 0 && r.u32(loc) === SIG_ZIP64_LOCATOR) {
			const z = r.u64(loc + 8);
			if (r.u32(z) !== SIG_ZIP64_EOCD) throw zipError("bad ZIP64 record");
			total = r.u64(z + 32);
			cdSize = r.u64(z + 40);
			cdOffset = r.u64(z + 48);
		}
	}

	// 前面拼了其他数据（自解压程序等）时记录的偏移整体偏小：按「中央目录紧贴 EOCD 之前」反推
	if (cdOffset + 4 > bytes.length || r.u32(cdOffset) !== SIG_CENTRAL) {
		const guess = eocd - cdSize;
		if (total > 0 && (guess < 0 || r.u32(guess) !== SIG_CENTRAL)) {
			throw zipError("central directory not found");
		}
		cdOffset = guess;
	}

	const entries: ArchiveEntry[] = [];
	const count = Math.min(total, MAX_ARCHIVE_ENTRIES);
	let off = cdOffset;
	for (let i = 0; i < count; i++) {
		if (r.u32(off) !== SIG_CENTRAL)
			throw zipError("bad central directory entry");
		const flags = r.u16(off + 8);
		const time = r.u16(off + 12);
		const date = r.u16(off + 14);
		let size = r.u32(off + 24);
		const nameLen = r.u16(off + 28);
		const extraLen = r.u16(off + 30);
		const commentLen = r.u16(off + 32);
		const rawName = r.slice(off + CENTRAL_SIZE, nameLen);
		let path = decodeZipName(rawName, (flags & 0x800) !== 0);
		let mtime = dosDateTime(date, time);

		// 扩展字段：逐个 [id u16][len u16][data]
		const extraEnd = off + CENTRAL_SIZE + nameLen + extraLen;
		let x = off + CENTRAL_SIZE + nameLen;
		while (x + 4 <= extraEnd) {
			const id = r.u16(x);
			const len = r.u16(x + 2);
			const data = x + 4;
			if (data + len > extraEnd) break;
			if (id === 0x0001 && size === U32_MAX && len >= 8) {
				// ZIP64 扩展：按「原始尺寸、压缩尺寸、偏移、磁盘号」顺序，只出现主记录中溢出的那些；原始尺寸总在最前
				size = r.u64(data);
			} else if (id === 0x7075 && len >= 5 && (flags & 0x800) === 0) {
				// Info-ZIP Unicode 路径：[版本 u8][原名 CRC32 u32][UTF-8 名]
				path = utf8Loose.decode(r.slice(data + 5, len - 5));
			} else if (id === 0x5455 && len >= 5 && (r.u8(data) & 1) !== 0) {
				// 扩展时间戳：标志位 0 表示带 mtime（UTC 秒）；中央目录里只会带 mtime
				mtime = r.u32(data + 1) * 1000;
			}
			x = data + len;
		}

		path = path.replace(/\\/g, "/");
		const dir = path.endsWith("/");
		entries.push({
			path,
			size: dir ? 0 : size,
			dir,
			...(mtime !== undefined ? { mtime } : {}),
		});
		off = extraEnd + commentLen;
	}
	return { entries, truncated: total > MAX_ARCHIVE_ENTRIES };
}

// ───────────────────────────── TAR ─────────────────────────────

const BLOCK = 512;

function tarError(detail: string): Error {
	return new Error(`Corrupt tar archive: ${detail}`);
}

/** 以 NUL 结尾（或占满字段）的字符串，按 UTF-8 解码。 */
function cstr(b: Uint8Array, off: number, len: number): string {
	let end = off;
	while (end < off + len && b[end] !== 0) end++;
	return utf8Loose.decode(b.subarray(off, end));
}

/**
 * 数值字段：通常是八进制 ASCII（可带前后空格/NUL）；GNU 扩展以首字节最高位置 1 表示 base-256 大端二进制。
 * 非法字符返回 NaN，由调用方判损坏。
 */
function tarNumber(b: Uint8Array, off: number, len: number): number {
	const first = b[off] ?? 0;
	if (first & 0x80) {
		let v = first & 0x7f;
		for (let i = 1; i < len; i++) v = v * 256 + (b[off + i] ?? 0);
		return v;
	}
	let v = 0;
	let seen = false;
	for (let i = 0; i < len; i++) {
		const c = b[off + i] ?? 0;
		if (c === 0 || c === 0x20) {
			if (seen) break;
			continue;
		}
		if (c < 0x30 || c > 0x37) return Number.NaN;
		v = v * 8 + (c - 0x30);
		seen = true;
	}
	return v;
}

function isZeroBlock(b: Uint8Array, off: number): boolean {
	for (let i = off; i < off + BLOCK; i++) if (b[i] !== 0) return false;
	return true;
}

/** 头部校验和：校验和字段按 8 个空格计，其余字节无符号累加。 */
function checksumOk(b: Uint8Array, off: number): boolean {
	const stored = tarNumber(b, off + 148, 8);
	if (Number.isNaN(stored)) return false;
	let sum = 0;
	for (let i = 0; i < BLOCK; i++) {
		sum += i >= 148 && i < 156 ? 0x20 : (b[off + i] ?? 0);
	}
	return sum === stored;
}

/** pax 扩展头：若干条 `<长度> <键>=<值>\n` 记录。 */
function parsePax(data: Uint8Array): Map<string, string> {
	const out = new Map<string, string>();
	let i = 0;
	while (i < data.length) {
		let sp = i;
		while (sp < data.length && data[sp] !== 0x20) sp++;
		const len = Number.parseInt(utf8Loose.decode(data.subarray(i, sp)), 10);
		if (!Number.isFinite(len) || len <= 0 || i + len > data.length) break;
		const rec = utf8Loose.decode(data.subarray(sp + 1, i + len - 1));
		const eq = rec.indexOf("=");
		if (eq > 0) out.set(rec.slice(0, eq), rec.slice(eq + 1));
		i += len;
	}
	return out;
}

/** 去掉开头的 `./`；归档根目录本身（`./`）返回空串，由调用方跳过。 */
function normalizeTarPath(p: string): string {
	let s = p;
	while (s.startsWith("./")) s = s.slice(2);
	return s;
}

/** 逐个 512 字节头部读取 tar 目录，遇到两个连续全零块（或数据结束）停止。 */
export function listTar(bytes: Uint8Array): ArchiveListing {
	const entries: ArchiveEntry[] = [];
	let off = 0;
	let longName: string | undefined;
	let pax: Map<string, string> | undefined;
	let truncated = false;
	let headers = 0;

	while (off + BLOCK <= bytes.length) {
		if (isZeroBlock(bytes, off)) {
			// 归档结束标记是两个全零块；单个全零块后若还有内容，跳过继续读
			if (off + 2 * BLOCK > bytes.length || isZeroBlock(bytes, off + BLOCK))
				break;
			off += BLOCK;
			continue;
		}
		if (!checksumOk(bytes, off)) throw tarError("header checksum mismatch");
		headers++;

		const type = String.fromCharCode(bytes[off + 156] ?? 0);
		let size = tarNumber(bytes, off + 124, 12);
		if (Number.isNaN(size)) throw tarError("bad size field");
		const paxSize = pax?.get("size");
		if (paxSize !== undefined && /^\d+$/.test(paxSize)) size = Number(paxSize);
		const dataStart = off + BLOCK;
		const dataEnd = dataStart + size;
		const next = dataStart + Math.ceil(size / BLOCK) * BLOCK;

		if (type === "L" || type === "x" || type === "g" || type === "K") {
			// 元数据头：内容作用于下一个真实条目（全局 pax `g` 与长链接名 `K` 对列表无意义，直接跳过）
			if (dataEnd > bytes.length) throw tarError("metadata extends past end");
			const data = bytes.subarray(dataStart, dataEnd);
			if (type === "L") longName = cstr(data, 0, data.length);
			else if (type === "x") pax = parsePax(data);
			off = next;
			continue;
		}

		if (entries.length >= MAX_ARCHIVE_ENTRIES) {
			truncated = true;
			break;
		}

		let path: string;
		const paxPath = pax?.get("path");
		if (paxPath !== undefined) path = paxPath;
		else if (longName !== undefined) path = longName;
		else {
			path = cstr(bytes, off, 100);
			// POSIX ustar（magic 为 "ustar\0"）才有 prefix 字段；GNU 格式（"ustar  "）在同一位置放的是别的数据
			const magic = cstr(bytes, off + 257, 6);
			if (magic === "ustar" && bytes[off + 262] === 0) {
				const prefix = cstr(bytes, off + 345, 155);
				if (prefix) path = `${prefix}/${path}`;
			}
		}
		path = normalizeTarPath(path);

		let mtime: number | undefined;
		const paxMtime = pax?.get("mtime");
		if (paxMtime !== undefined && Number.isFinite(Number(paxMtime))) {
			mtime = Math.round(Number(paxMtime) * 1000);
		} else {
			const m = tarNumber(bytes, off + 136, 12);
			if (!Number.isNaN(m) && m > 0) mtime = m * 1000;
		}
		longName = undefined;
		pax = undefined;

		if (path) {
			const dir = type === "5" || path.endsWith("/");
			// 只有普通文件（'0'、旧式 NUL、连续文件 '7'）的尺寸是内容长度；链接、设备等记为 0
			const isFile = type === "0" || type === "\0" || type === "7";
			entries.push({
				path,
				size: dir || !isFile ? 0 : size,
				dir,
				...(mtime !== undefined ? { mtime } : {}),
			});
		}
		// 内容被截断的归档：保留已读到的条目，到此为止
		if (dataEnd > bytes.length) break;
		off = next;
	}

	if (headers === 0 && bytes.length > 0 && !isZeroPrefix(bytes)) {
		throw tarError("no valid header");
	}
	return { entries, truncated };
}

/** 不足一个块、但全是零的输入（空归档被截短）不算损坏。 */
function isZeroPrefix(b: Uint8Array): boolean {
	const n = Math.min(b.length, BLOCK);
	for (let i = 0; i < n; i++) if (b[i] !== 0) return false;
	return true;
}
