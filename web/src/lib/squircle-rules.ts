/**
 * 角形降级的共享规则表：同一组声明（clip-path / border-image / --sq-s）在同一种伪元素上只写一条规则，
 * 盒子挂上该规则对应的空属性（`data-sq<编号>`）即可套用。
 *
 * 比逐元素写行内样式便宜：裁剪与环带与盒子尺寸无关，同半径、同边框的盒子共用一条规则，解析一次，
 * 样式重算时也能互相复用；属性名各不相同，引擎按属性名分桶匹配，规则再多每个元素也只看自己挂的那条。
 * 规则用 CSSOM 逐条插入与删除，不整表重建。
 */

/** 盒子：元素本身或它的 ::before / ::after。 */
export type Pseudo = "" | "::before" | "::after";

/** 规则对应的属性名形如 data-sq12（与根元素上的 data-squircle 区分）。 */
export const RULE_ATTR = /^data-sq\d+$/;

/** 不再被引用的规则攒到这么多条再一起删（尺寸在两种形状间来回时不必反复插删）。 */
const SWEEP_AT = 64;

interface Entry {
	attr: string;
	key: string;
	rule: CSSRule;
	refs: number;
}

export class ShapeRules {
	readonly #sheet: CSSStyleSheet;
	/** 伪元素 + 声明 → 规则。 */
	readonly #byKey = new Map<string, Entry>();
	readonly #byAttr = new Map<string, Entry>();
	/** 引用数降到 0、等着 sweep 的规则。 */
	readonly #unused = new Set<Entry>();
	#nextId = 1;

	constructor(sheet: CSSStyleSheet) {
		this.#sheet = sheet;
	}

	/**
	 * 取得声明 decl 用在 pseudo 上的属性名（没有就插入一条规则），引用数 +1。
	 * 引擎不认这组声明（insertRule 抛错）时返回 ""：该盒子保持原样（包络圆），不影响其它盒子。
	 */
	acquire(decl: string, pseudo: Pseudo): string {
		const key = `${pseudo}{${decl}`;
		let entry = this.#byKey.get(key);
		if (!entry) {
			const attr = `data-sq${this.#nextId++}`;
			let rule: CSSRule | null | undefined;
			try {
				const index = this.#sheet.insertRule(
					`[${attr}]${pseudo}{${decl}}`,
					this.#sheet.cssRules.length,
				);
				rule = this.#sheet.cssRules.item(index);
			} catch {
				return "";
			}
			if (!rule) return "";
			entry = { attr, key, rule, refs: 0 };
			this.#byKey.set(key, entry);
			this.#byAttr.set(attr, entry);
		}
		this.#unused.delete(entry);
		entry.refs++;
		return entry.attr;
	}

	/** 引用数 −1；降到 0 的规则先留着，攒够 SWEEP_AT 条再一起删。 */
	release(attr: string): void {
		const entry = this.#byAttr.get(attr);
		if (!entry || entry.refs === 0) return;
		entry.refs--;
		if (entry.refs > 0) return;
		this.#unused.add(entry);
		if (this.#unused.size >= SWEEP_AT) this.sweep();
	}

	/** 删掉所有不再被引用的规则（从后往前删，下标不受影响）。 */
	sweep(): void {
		if (this.#unused.size === 0) return;
		const dead = new Set<CSSRule>();
		for (const entry of this.#unused) {
			dead.add(entry.rule);
			this.#byAttr.delete(entry.attr);
			this.#byKey.delete(entry.key);
		}
		this.#unused.clear();
		const rules = this.#sheet.cssRules;
		for (let i = rules.length - 1; i >= 0; i--) {
			const rule = rules.item(i);
			if (rule && dead.has(rule)) this.#sheet.deleteRule(i);
		}
	}
}
