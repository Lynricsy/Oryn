// 由 oryn-agent `scripts/oryn/emit-rpc-dts.ts` 自动生成，请勿手改。
// 来源：packages/coding-agent/src/modes/rpc/rpc-types.ts

declare namespace Bun {
	type FormDataEntryValue = File | string;
}
type Buffer = Uint8Array;
interface ErrorContext {
	readonly code: string;
	readonly path: readonly PropertyKey[];
	readonly propString: string;
	readonly data: unknown;
	readonly expected: string;
	readonly actual: string;
	readonly problem: string;
	readonly description: string;
	readonly rule?: unknown;
}
interface ErrorConfig {
	readonly expected?: string | ((context: ErrorContext) => string);
	readonly actual?: string | ((data: unknown) => string);
	readonly problem?: string | ((context: ErrorContext) => string);
	readonly message?: string | ((context: ErrorContext) => string);
	/** Internal: custom predicate expectations display the offending value rather than its domain. */
	readonly preserveActual?: boolean;
}
declare class OmpError {
	#private;
	/** Property path from the root to the failing value (empty at root). */
	readonly path: PropertyKey[];
	/** The value that failed validation. */
	readonly data: unknown;
	constructor(
	/** Property path from the root to the failing value (empty at root). */
	path: PropertyKey[], expected: string, 
	/** The value that failed validation. */
	data: unknown, config?: ErrorConfig);
	/** Prefix this failure when a nested schema delegates validation. */
	prefix(key: PropertyKey): this;
	/** Apply schema-local formatting to this failure. */
	configure(config: ErrorConfig): this;
	/** Stable category for programmatic error handling. */
	get code(): string;
	/** Human-readable expectation, including a configured override. */
	get expected(): string;
	/** Short description of the received value, e.g. `"a number"` or `"missing"`. */
	get actual(): string;
	/** Path-less problem statement: `must be <expected> (was <actual>)`. */
	get problem(): string;
	get message(): string;
	toString(): string;
}
type StoredPath = PropertyKey[] | PropertyKey | undefined;
declare class OmpErrors implements Iterable<OmpError> {
	#private;
	constructor(path: StoredPath, expected: string, data: unknown, config?: ErrorConfig);
	get length(): number;
	get 0(): OmpError;
	static single(path: PropertyKey[], expected: string, data: unknown, config?: ErrorConfig): OmpErrors;
	/** Append all failures from `other`, preserving traversal order. */
	append(other: OmpErrors): this;
	/** Prefix every failure path with `key` when nesting sub-schemas. */
	prefix(key: PropertyKey): this;
	/** Apply schema-local message formatting without rebuilding failures. */
	configure(config: ErrorConfig): this;
	get byPath(): Readonly<Record<string, OmpError>>;
	map<result>(fn: (error: OmpError, index: number, errors: OmpErrors) => result): result[];
	filter(fn: (error: OmpError, index: number, errors: OmpErrors) => unknown): OmpError[];
	[Symbol.iterator](): IterableIterator<OmpError>;
	/** @internal Render multiple branch failures as alternatives rather than independent failures. */
	asAlternatives(): this;
	get summary(): string;
	toString(): string;
	throw(): never;
}
type Whitespace = " " | "\n" | "\r" | "\t";
type TrimLeft<s extends string> = s extends `${Whitespace}${infer rest}` ? TrimLeft<rest> : s;
type TrimRight<s extends string> = s extends `${infer rest}${Whitespace}` ? TrimRight<rest> : s;
type Trim<s extends string> = TrimLeft<TrimRight<s>>;
type ArkAny = ReturnType<typeof JSON.parse>;
interface PrimitiveMap {
	string: string;
	"string.url": string;
	number: number;
	"number.integer": number;
	"number.epoch": number;
	"number.safe": number;
	"number.NaN": number;
	"number.Infinity": number;
	"number.NegativeInfinity": number;
	boolean: boolean;
	null: null;
	undefined: undefined;
	unknown: unknown;
	"unknown.any": ArkAny;
	any: unknown;
	object: object;
	bigint: bigint;
	symbol: symbol;
	Key: PropertyKey;
	Date: Date;
	Array: unknown[];
	Function: Function;
	RegExp: RegExp;
	File: File;
	Error: Error;
	Set: Set<unknown>;
	Map: Map<unknown, unknown>;
	WeakSet: WeakSet<WeakKey>;
	WeakMap: WeakMap<WeakKey, unknown>;
	Promise: Promise<unknown>;
	FormData: FormData;
	"object.json": unknown;
	true: true;
	false: false;
}
type Merge<left, right> = left extends object ? right extends object ? Omit<left, keyof right> & right : never : never;
type InferUtility<s extends string> = s extends `Record<${string},${infer value}>` ? Record<string, InferString<value>> : s extends `Array<${infer element}>` | `Array.liftFrom<${infer element}>` ? InferString<element>[] : s extends `Partial<${infer value}>` ? Partial<InferString<value>> : s extends `Required<${infer value}>` ? Required<InferString<value>> : s extends `Pick<${infer value},${infer keys}>` ? Pick<InferString<value>, Extract<InferString<keys>, keyof InferString<value>>> : s extends `Omit<${infer value},${infer keys}>` ? Omit<InferString<value>, Extract<InferString<keys>, keyof InferString<value>>> : s extends `Merge<${infer left},${infer right}>` ? Merge<InferString<left>, InferString<right>> : never;
type InferParse<s extends string> = s extends "string.numeric.parse" | "string.integer.parse" | "parse.number" | "parse.integer" ? number : s extends "string.date.parse" | "string.date.iso.parse" | "string.date.epoch.parse" | "parse.date" ? Date : s extends "string.url.parse" | "parse.url" ? URL : s extends "string.json.parse" | "parse.json" ? unknown : s extends "object.json.stringify" ? string : s extends "FormData.parse" ? Record<string, Bun.FormDataEntryValue | Bun.FormDataEntryValue[]> : s extends "parse.boolean" ? boolean : s extends "parse.bigint" ? bigint : never;
type InferMember<member extends string> = Trim<member> extends infer s extends string ? InferMemberTrimmed<s> : unknown;
type InferMemberTrimmed<s extends string> = s extends `(${infer inner})` ? InferString<inner> : s extends `${infer element}[]` ? InferMember<element>[] : s extends `'${infer literal}'` | `"${infer literal}"` ? literal : s extends `d'${string}'` | `d"${string}"` ? Date : s extends `\`${string}\`` ? string : s extends `/${string}/${string}` | `/${string}/` ? string : s extends `${infer literal extends number}` ? literal : s extends keyof PrimitiveMap ? PrimitiveMap[s] : [
	InferParse<s>
] extends [
	never
] ? InferUtility<s> extends infer utility ? [
	utility
] extends [
	never
] ? InferMemberFallback<s> : utility : unknown : InferParse<s>;
type InferMemberFallback<s extends string> = s extends `string.${string}` ? string : s extends `${string}Date${string}` ? Date : s extends `${string}string${string}` ? string : s extends `${string}number${string}` ? number : unknown;
type InferUnion<s extends string, result = never> = s extends `${infer head}|${infer tail}` ? InferUnion<tail, result | InferMember<head>> : result | InferMember<s>;
type InferMemberIn<member extends string> = Trim<member> extends infer s extends string ? s extends `${string}.parse` | `parse.${string}` ? string : InferMemberTrimmed<s> : unknown;
type InferUnionIn<s extends string, result = never> = s extends `${infer head}|${infer tail}` ? InferUnionIn<tail, result | InferMemberIn<head>> : result | InferMemberIn<s>;
type HasInlineDefault<s extends string> = s extends `${string}=${string}` ? s extends `${string}<${string}` | `${string}>${string}` ? false : true : false;
type WithoutInlineDefault<s extends string> = HasInlineDefault<s> extends true ? (s extends `${infer base}=${string}` ? Trim<base> : s) : s;
type InferStringOutput<s extends string> = InferUnion<s>;
type InferString<s extends string> = WithoutInlineDefault<Trim<s>> extends infer trimmed extends string ? trimmed extends `${infer base}?` ? InferString<base> : trimmed extends `(${infer inner})[]` ? InferUnion<inner>[] : InferStringOutput<trimmed> : unknown;
type InferStringIn<s extends string> = WithoutInlineDefault<Trim<s>> extends infer trimmed extends string ? trimmed extends `${infer base}?` ? InferStringIn<base> : trimmed extends `(${infer inner})[]` ? InferUnionIn<inner>[] : InferUnionIn<trimmed> : unknown;
type HasDefault<def> = def extends string ? HasInlineDefault<def> : def extends readonly [
	unknown,
	"=",
	unknown
] ? true : def extends {
	readonly hasDefault: true;
} ? true : false;
type DefinitionKeys<def extends object> = Exclude<keyof def, "+" | "[string]" | "...">;
type IsOptionalProp<key, def> = key extends `${string}?` ? true : def extends string ? Trim<def> extends `${string}?` ? true : false : def extends readonly [
	unknown,
	"?"
] ? true : false;
type PropName<key extends PropertyKey> = key extends `${infer name}?` ? name : key;
type UnwrapProperty<def> = def extends readonly [
	infer value,
	"?" | "=",
	...unknown[]
] ? value : def;
type Simplify<t> = {
	[key in keyof t]: t[key];
};
type OutputRequired<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? HasDefault<def[key]> extends true ? PropName<key> : never : PropName<key>]-?: InferDef<UnwrapProperty<def[key]>>;
};
type OutputOptional<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? HasDefault<def[key]> extends true ? never : PropName<key> : never]?: InferDef<UnwrapProperty<def[key]>>;
};
type InputRequired<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? never : HasDefault<def[key]> extends true ? never : PropName<key>]-?: InferDefIn<UnwrapProperty<def[key]>>;
};
type InputOptional<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? PropName<key> : HasDefault<def[key]> extends true ? PropName<key> : never]?: InferDefIn<UnwrapProperty<def[key]>>;
};
interface DefBox<def> {
	readonly out: InferDef<def>;
	readonly in: InferDefIn<def>;
}
type OutputSpread<def extends object> = "..." extends keyof def ? DefBox<def["..."]>["out"] : unknown;
type InputSpread<def extends object> = "..." extends keyof def ? DefBox<def["..."]>["in"] : unknown;
type OutputIndex<def extends object> = "[string]" extends keyof def ? Record<string, DefBox<def["[string]"]>["out"]> : unknown;
type InputIndex<def extends object> = "[string]" extends keyof def ? Record<string, DefBox<def["[string]"]>["in"]> : unknown;
type InferObject<def extends object> = "[string]" extends keyof def ? [
	DefinitionKeys<def>
] extends [
	never
] ? Record<string, DefBox<def["[string]"]>["out"]> : Simplify<OutputRequired<def> & OutputOptional<def> & OutputSpread<def>> & OutputIndex<def> : Simplify<OutputRequired<def> & OutputOptional<def> & OutputSpread<def>>;
type InferObjectIn<def extends object> = "[string]" extends keyof def ? [
	DefinitionKeys<def>
] extends [
	never
] ? Record<string, DefBox<def["[string]"]>["in"]> : Simplify<InputRequired<def> & InputOptional<def> & InputSpread<def>> & InputIndex<def> : Simplify<InputRequired<def> & InputOptional<def> & InputSpread<def>>;
type InferLiteralDef<def> = def extends {
	readonly infer: infer output;
} ? output : def extends string ? InferString<def> : def extends object ? InferObjectLiteral<def> : unknown;
type InferLiteralDefIn<def> = def extends {
	readonly inferIn: infer input;
} ? input : def extends string ? InferStringIn<def> : def extends object ? InferObjectLiteralIn<def> : unknown;
type LiteralRequired<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? HasDefault<def[key]> extends true ? PropName<key> : never : PropName<key>]-?: InferLiteralDef<UnwrapProperty<def[key]>>;
};
type LiteralOptional<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? HasDefault<def[key]> extends true ? never : PropName<key> : never]?: InferLiteralDef<UnwrapProperty<def[key]>>;
};
type LiteralRequiredIn<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? never : HasDefault<def[key]> extends true ? never : PropName<key>]-?: InferLiteralDefIn<UnwrapProperty<def[key]>>;
};
type LiteralOptionalIn<def extends object> = {
	-readonly [key in DefinitionKeys<def> as IsOptionalProp<key, def[key]> extends true ? PropName<key> : HasDefault<def[key]> extends true ? PropName<key> : never]?: InferLiteralDefIn<UnwrapProperty<def[key]>>;
};
type InferObjectLiteral<def extends object> = Simplify<LiteralRequired<def> & LiteralOptional<def>>;
type InferObjectLiteralIn<def extends object> = Simplify<LiteralRequiredIn<def> & LiteralOptionalIn<def>>;
type InstanceOf<ctor> = ctor extends abstract new (...args: never[]) => infer instance ? instance : never;
type SpreadOutput<def> = InferDef<def> extends readonly (infer element)[] ? element[] : never[];
type SpreadInput<def> = InferDefIn<def> extends readonly (infer element)[] ? element[] : never[];
type InferTupleOutput<defs extends readonly unknown[], result extends unknown[] = [
]> = defs extends readonly [
] ? result : defs extends readonly [
	"...",
	infer spread,
	...infer rest
] ? [
	...result,
	...SpreadOutput<spread>,
	...InferTupleOutput<rest>
] : defs extends readonly [
	infer head,
	...infer rest
] ? head extends readonly [
	infer value,
	"?"
] ? InferTupleOutput<rest, [
	...result,
	InferDef<value>?
]> : head extends readonly [
	infer value,
	"=",
	unknown
] ? InferTupleOutput<rest, [
	...result,
	InferDef<value>
]> : InferTupleOutput<rest, [
	...result,
	InferDef<head>
]> : result;
type InferTupleInput<defs extends readonly unknown[], result extends unknown[] = [
]> = defs extends readonly [
] ? result : defs extends readonly [
	"...",
	infer spread,
	...infer rest
] ? [
	...result,
	...SpreadInput<spread>,
	...InferTupleInput<rest>
] : defs extends readonly [
	infer head,
	...infer rest
] ? head extends readonly [
	infer value,
	"?" | "=",
	...unknown[]
] ? InferTupleInput<rest, [
	...result,
	InferDefIn<value>?
]> : InferTupleInput<rest, [
	...result,
	InferDefIn<head>
]> : result;
type IsAny<def> = 0 extends 1 & def ? true : false;
type InferDef<def = unknown> = IsAny<def> extends true ? unknown : def extends {
	readonly infer: infer output;
} ? output : def extends string ? InferString<def> : def extends RegExp ? string : def extends readonly [
	infer element,
	"[]"
] ? InferDef<element>[] : def extends readonly [
	infer left,
	"|",
	infer right
] ? InferDef<left> | InferDef<right> : def extends readonly [
	infer left,
	"&",
	infer right
] ? InferDef<left> & InferDef<right> : def extends readonly [
	unknown,
	"=>",
	(...args: never[]) => infer output
] ? output : def extends readonly [
	unknown,
	"|>",
	infer output
] ? InferDef<output> : def extends readonly [
	infer base,
	":",
	unknown
] | readonly [
	infer base,
	"@",
	unknown
] ? InferDef<base> : def extends readonly [
	"keyof",
	infer base
] ? keyof InferDef<base> : def extends readonly [
	"instanceof",
	...infer constructors
] ? InstanceOf<constructors[number]> : def extends readonly [
	"===",
	...infer values
] ? values[number] : def extends readonly unknown[] ? InferTupleOutput<def> : def extends object ? InferObject<def> : unknown;
type InferDefIn<def = unknown> = IsAny<def> extends true ? unknown : def extends {
	readonly inferIn: infer input;
} ? input : def extends string ? InferStringIn<def> : def extends RegExp ? string : def extends readonly [
	infer element,
	"[]"
] ? InferDefIn<element>[] : def extends readonly [
	infer left,
	"|",
	infer right
] ? InferDefIn<left> | InferDefIn<right> : def extends readonly [
	infer left,
	"&",
	infer right
] ? InferDefIn<left> & InferDefIn<right> : def extends readonly [
	infer input,
	"=>",
	unknown
] | readonly [
	infer input,
	"|>",
	unknown
] ? InferDefIn<input> : def extends readonly [
	infer base,
	":",
	unknown
] | readonly [
	infer base,
	"@",
	unknown
] ? InferDefIn<base> : def extends readonly [
	"keyof",
	infer base
] ? keyof InferDefIn<base> : def extends readonly [
	"instanceof",
	...infer constructors
] ? InstanceOf<constructors[number]> : def extends readonly [
	"===",
	...infer values
] ? values[number] : def extends readonly unknown[] ? InferTupleInput<def> : def extends object ? InferObjectIn<def> : unknown;
declare const IR_BRAND: unique symbol;
declare const kMorph: unique symbol;
declare const kMorphOwner: unique symbol;
declare const kAlias: unique symbol;
declare const kAliasOwner: unique symbol;
declare const kSimple: unique symbol;
declare const kSimpleOwner: unique symbol;
interface IRAnalysis {
	[kMorph]?: boolean;
	[kMorphOwner]?: object;
	[kAlias]?: boolean;
	[kAliasOwner]?: object;
	[kSimple]?: boolean;
	[kSimpleOwner]?: object;
	/** Node-local metadata used for shallow error formatting. */
	cfg?: ErrorConfig;
	/** True when `desc` was derived from the node itself rather than authored via `.describe()`. */
	descAuto?: boolean;
}
interface EmbeddableSchema {
	[IR_BRAND]: true;
	/** Structural IR of the schema (base type when runtime steps exist). */
	ir: IR;
	/** True when the schema carries `.pipe()`/`.narrow()` steps. */
	hasSteps: boolean;
	/** Output IR of the last `.to(target)` step, when statically known. */
	stepOut?: IR;
	/** True when the last pipe step is bare — output shape statically unknown. */
	opaqueOutput?: boolean;
	/** `.default()` payload; a function is a factory invoked per fill. */
	defaultValue?: unknown;
	hasDefault: boolean;
	/** Precomputed output for a non-factory default after validation and morphs. */
	defaultOutput?: unknown;
	hasDefaultOutput?: boolean;
	/** `.describe()` annotation, emitted into JSON Schema. */
	description?: string;
	/** Full validate+morph pipeline (identical to calling the schema). */
	run(value: unknown, path?: readonly PropertyKey[]): unknown;
}
type Extras = "keep" | "reject" | "delete";
type Constructor = abstract new (...args: never[]) => object;
interface MorphContext {
	/** Return a validation error at the current path. */
	error(expectation: string): OmpErrors;
	/** Alias of `error` matching ArkType's rejection vocabulary. */
	reject(expectation: string): OmpErrors;
}
interface TupleItemIR {
	val: IR;
	opt: boolean;
	def?: unknown;
	defFactory?: boolean;
	hasDefault?: boolean;
	/** True once the default has been validated and static morph output precomputed. */
	defValidated?: boolean;
}
interface TupleIR {
	k: "tuple";
	prefix: TupleItemIR[];
	variadic?: IR;
	postfix: IR[];
	desc?: string;
}
type IR = IRAnalysis & ({
	k: "unknown";
	desc?: string;
} | {
	k: "null";
	desc?: string;
} | {
	k: "undefined";
	desc?: string;
} | {
	k: "boolean";
	desc?: string;
} | {
	k: "bigint";
	desc?: string;
} | {
	k: "symbol";
	desc?: string;
} | {
	k: "never";
	desc?: string;
}
/** Any non-null object (the bare `object` keyword). */
 | {
	k: "anyobject";
	desc?: string;
} | {
	k: "string";
	min?: number;
	max?: number;
	url?: boolean;
	desc?: string;
} | {
	k: "number";
	min?: number;
	max?: number;
	xmin?: boolean;
	xmax?: boolean;
	int?: boolean;
	divisor?: number;
	desc?: string;
} | {
	k: "lit";
	v: unknown;
	desc?: string;
} | {
	k: "union";
	members: IR[];
	desc?: string;
} | {
	k: "intersection";
	members: IR[];
	desc?: string;
} | {
	k: "array";
	el: IR;
	min?: number;
	max?: number;
	desc?: string;
} | TupleIR | {
	k: "object";
	props: PropIR[];
	index?: IR;
	symbolIndex?: IR;
	patternIndexes?: {
		key: IR;
		val: IR;
	}[];
	extras: Extras;
	desc?: string;
} | {
	k: "refine";
	base: IR;
	pred: (value: unknown) => boolean | OmpErrors;
	/** Allocation-free equivalent of `pred(value) === true` for boolean-only paths (`allows`, unions). */
	check?: (value: unknown) => boolean;
	expected: string;
	json?: Record<string, unknown>;
	desc?: string;
} | {
	k: "morph";
	input: IR;
	fn: (value: unknown, context: MorphContext) => unknown;
	out?: IR;
	desc?: string;
} | {
	k: "instance";
	ctor: Constructor;
	expected: string;
	desc?: string;
} | {
	k: "alias";
	name: string;
	resolve: () => IR;
	desc?: string;
}
/** Embedded schema with runtime steps; validated by calling `run`. */
 | {
	k: "sub";
	schema: EmbeddableSchema;
	desc?: string;
});
interface PropIR {
	key: PropertyKey;
	opt: boolean;
	val: IR;
	/** Default payload (value, or factory when `defFactory`); missing key is filled. */
	def?: unknown;
	defFactory?: boolean;
	hasDefault?: boolean;
	/** True once the default has been validated and static morph output precomputed. */
	defValidated?: boolean;
}
type Def = string | RegExp | Date | EmbeddableSchema | readonly unknown[] | {
	readonly [k: string]: unknown;
};
interface JsonSchemaOptions {
	description?: string;
	target?: string;
	dialect?: string | null;
	/**
	 * Which side of morphs and defaults to describe:
	 * - `'input'` — accepted payloads: morphs emit their input shape, defaulted
	 *   properties are optional (with `default` annotations).
	 * - `'output'` — produced values: morphs emit their output shape, defaulted
	 *   properties are required (always present after validation).
	 * Unset keeps the hybrid legacy behavior (morph output, defaults optional).
	 */
	io?: "input" | "output";
	fallback?: (context: {
		base: Record<string, unknown>;
	}) => unknown;
}
interface NarrowErrorInput {
	readonly expected: string;
	readonly actual?: unknown;
	readonly path?: readonly PropertyKey[];
	readonly relativePath?: readonly PropertyKey[];
}
interface NarrowContext {
	readonly path: readonly PropertyKey[];
	error(error: string | NarrowErrorInput): OmpErrors;
	mustBe(expectation: string): false;
	reject(problem: string | NarrowErrorInput): OmpErrors | false;
}
interface SchemaConfig extends ErrorConfig {
	readonly description?: string;
}
interface ToJsonSchemaOptions extends JsonSchemaOptions {
}
declare const brand: unique symbol;
type Brand<t, name extends string> = t & {
	readonly [brand]: name;
};
interface SchemaInference<out t, i = t> {
	readonly [IR_BRAND]: true;
	readonly infer: t;
	readonly inferIn: i;
}
interface TypeProperty {
	readonly kind: "required" | "optional";
	readonly key: PropertyKey;
	readonly value: FluentType<unknown>;
	readonly default?: unknown;
	readonly meta: Readonly<Record<string, unknown>>;
}
interface SelectedNode {
	readonly kind: string;
	readonly node: IR;
	readonly unit?: unknown;
}
interface StandardSchemaV1<Input = unknown, Output = Input> {
	readonly "~standard": StandardSchemaV1.Props<Input, Output>;
}
declare namespace StandardSchemaV1 {
	interface Props<Input = unknown, Output = Input> {
		readonly version: 1;
		readonly vendor: string;
		readonly validate: (value: unknown) => Result<Output> | Promise<Result<Output>>;
		readonly types?: Types<Input, Output> | undefined;
		readonly jsonSchema: {
			readonly input: (options: StandardJsonSchemaOptions) => Record<string, unknown>;
			readonly output: (options: StandardJsonSchemaOptions) => Record<string, unknown>;
		};
	}
	type Result<Output> = SuccessResult<Output> | FailureResult;
	interface SuccessResult<Output> {
		readonly value: Output;
		readonly issues?: undefined;
	}
	interface FailureResult {
		readonly issues: readonly Issue[];
	}
	interface Issue {
		readonly message: string;
		readonly path?: readonly PropertyKey[] | undefined;
	}
	interface Types<Input = unknown, Output = Input> {
		readonly input: Input;
		readonly output: Output;
	}
}
interface StandardJsonSchemaOptions {
	readonly target: "draft-2020-12" | "draft-07" | string;
	readonly libraryOptions?: {
		readonly dialect?: string | null;
		readonly fallback?: JsonSchemaOptions["fallback"];
	};
}
interface Type<out t = unknown, i = t> {
	(data: unknown): t | OmpErrors;
	readonly [IR_BRAND]: true;
	/** Structural IR (base type; runtime steps live in `steps`). */
	readonly ir: IR;
	/** `.pipe()` / `.narrow()` steps applied after structural validation. */
	readonly hasSteps: boolean;
	readonly hasDefault: boolean;
	readonly defaultValue?: unknown;
	readonly description?: string;
	/** Canonical ArkType-compatible expression for diagnostics. */
	readonly expression: string;
	/** Canonical structural node representation. */
	readonly json: unknown;
	/** Full validate+morph pipeline; identical to calling the schema. */
	readonly run: (data: unknown) => unknown;
	/** ArkType-compatible inference alias (type-only; undefined at runtime). */
	readonly t: t;
	/** Scope that parsed this schema (or the ambient Ark-compatible scope). */
	readonly $: TypeScope | {
		readonly internal: {
			readonly name: "ark";
		};
	};
	/** Inference-only output type (no runtime value). */
	readonly infer: t;
	/** Standalone validator for the schema's accepted input. */
	readonly in: FluentType<i>;
	/** Standalone validator for its known output, or `unknown` after an opaque morph. */
	readonly out: FluentType<t>;
	/** Inference-only input type (no runtime value). */
	readonly inferIn: i;
	/** Structural + narrow check without running pipes. */
	allows(data: unknown): data is i;
	/** Validate and return output, throwing `TraversalError` on failure. */
	assert(data: unknown): t;
	/** Validate a statically typed input and return its output. */
	from(data: i): t;
	/** JSON Schema for this schema's structural base. */
	toJsonSchema(options?: ToJsonSchemaOptions): Record<string, unknown>;
	/** Standard Schema V1 interop (synchronous validation). */
	readonly "~standard": StandardSchemaV1.Props<i, t>;
}
type MergeTypes<left, right> = left extends object ? right extends object ? Omit<left, keyof right> & right : right : right;
type SimplifyNary<t> = t extends object ? {
	[key in keyof t]: t[key];
} : t;
type UnionToIntersection<union> = (union extends unknown ? (value: union) => void : never) extends (value: infer intersection) => void ? intersection : never;
type NaryOrOutput<definitions extends readonly unknown[]> = InferDef<definitions[number]>;
type NaryOrInput<definitions extends readonly unknown[]> = InferDefIn<definitions[number]>;
type NaryAndOutput<definitions extends readonly unknown[]> = definitions extends readonly [
] ? unknown : SimplifyNary<UnionToIntersection<InferDef<definitions[number]>>>;
type NaryAndInput<definitions extends readonly unknown[]> = definitions extends readonly [
] ? unknown : SimplifyNary<UnionToIntersection<InferDefIn<definitions[number]>>>;
type ReduceNaryMergeOutput<definitions extends readonly unknown[], result = {}> = definitions extends readonly [
	infer head,
	...infer tail
] ? ReduceNaryMergeOutput<tail, SimplifyNary<MergeTypes<result, InferDef<head>>>> : definitions extends readonly [
] ? result : {};
type ReduceNaryMergeInput<definitions extends readonly unknown[], result = {}> = definitions extends readonly [
	infer head,
	...infer tail
] ? ReduceNaryMergeInput<tail, SimplifyNary<MergeTypes<result, InferDefIn<head>>>> : definitions extends readonly [
] ? result : {};
type NaryMergeOutput<definitions extends readonly unknown[]> = definitions extends readonly [
] ? object : ReduceNaryMergeOutput<definitions>;
type NaryMergeInput<definitions extends readonly unknown[]> = definitions extends readonly [
] ? object : ReduceNaryMergeInput<definitions>;
type PipeItemOutput<item> = item extends SchemaInference<infer output, unknown> ? output : item extends (data: never, ...arguments_: never[]) => infer output ? Exclude<output, OmpErrors> : InferDef<item>;
type NaryPipeOutput<items extends readonly unknown[]> = items extends readonly [
	...(readonly unknown[]),
	infer last
] ? PipeItemOutput<last> : unknown;
type NaryPipeInput<items extends readonly unknown[]> = items extends readonly [
	infer first,
	...(readonly unknown[])
] ? first extends SchemaInference<unknown, infer input> ? input : first extends (data: infer input, ...arguments_: never[]) => unknown ? input : InferDefIn<first> : unknown;
interface FluentMethods<t, i> {
	describe(description: string): FluentType<t, i>;
	configure(config: SchemaConfig, selector?: "self" | ConfigureSelector): FluentType<t, i>;
	default(value: i | (() => i)): FluentType<t, i | undefined>;
	optional(): readonly [
		SchemaInference<t, i>,
		"?"
	];
	or<r, ri>(def: SchemaInference<r, ri>): FluentType<t | r, i | ri>;
	or<const def extends string>(def: def): FluentType<t | InferString<def>, i | InferString<def>>;
	or<const def extends Record<string, unknown>>(def: def): FluentType<t | InferObjectLiteral<def>, i | InferObjectLiteralIn<def>>;
	or(def: Def): FluentType<unknown>;
	and<r, ri>(def: SchemaInference<r, ri>): FluentType<t & r, i & ri>;
	and<const def extends Record<string, unknown>>(def: def): FluentType<t & InferObjectLiteral<def>, i & InferObjectLiteralIn<def>>;
	and(def: Def): FluentType<unknown>;
	equals(def: Def): boolean;
	ifEquals(def: Def): FluentType<t, i> | undefined;
	ifExtends(def: Def): FluentType<t, i> | undefined;
	extends(def: Def): boolean;
	overlaps(def: Def): boolean;
	distribute<r>(mapper: (branch: FluentType<unknown>) => SchemaInference<r>, reducer?: (branches: readonly SchemaInference<r>[]) => SchemaInference<unknown>): FluentType<r>;
	select(kind: string): readonly SelectedNode[];
	array(): FluentType<t[], i[]>;
	atLeastLength(bound: number): FluentType<t, i>;
	atMostLength(bound: number): FluentType<t, i>;
	moreThanLength(bound: number): FluentType<t, i>;
	lessThanLength(bound: number): FluentType<t, i>;
	exactlyLength(bound: number): FluentType<t, i>;
	atLeast(bound: number): FluentType<t, i>;
	atMost(bound: number): FluentType<t, i>;
	moreThan(bound: number): FluentType<t, i>;
	lessThan(bound: number): FluentType<t, i>;
	divisibleBy(divisor: number): FluentType<t, i>;
	positive(): FluentType<t, i>;
	negative(): FluentType<t, i>;
	nonNegative(): FluentType<t, i>;
	nonPositive(): FluentType<t, i>;
	matching(pattern: RegExp): FluentType<t, i>;
	atOrAfter(bound: Date | number): FluentType<t, i>;
	atOrBefore(bound: Date | number): FluentType<t, i>;
	laterThan(bound: Date | number): FluentType<t, i>;
	earlierThan(bound: Date | number): FluentType<t, i>;
	readonly pipe: PipeMethod<t, i>;
	to<const def>(def: def): FluentType<InferDef<def>, i>;
	filter<narrowed extends i>(fn: (data: i, ctx: NarrowContext) => data is narrowed): FluentType<t, narrowed>;
	filter(fn: (data: i, ctx: NarrowContext) => boolean | OmpErrors): FluentType<t, i>;
	narrow<narrowed extends t>(fn: (data: t, ctx: NarrowContext) => data is narrowed): FluentType<narrowed, i>;
	narrow(fn: (data: t, ctx: NarrowContext) => boolean | OmpErrors): FluentType<t, i>;
	brand<const name extends string>(name: name): FluentType<Brand<t, name>, i>;
	as<castTo>(): FluentType<castTo, i>;
	readonly(): FluentType<Readonly<t>, i>;
	extract<r, ri>(def: SchemaInference<r, ri>): FluentType<Extract<t, r>, Extract<i, ri>>;
	extract<const def extends string>(def: def): FluentType<Extract<t, InferString<def>>, Extract<i, InferString<def>>>;
	extract(def: Def): FluentType<unknown>;
	exclude<r, ri>(def: SchemaInference<r, ri>): FluentType<Exclude<t, r>, Exclude<i, ri>>;
	exclude<const def extends string>(def: def): FluentType<Exclude<t, InferString<def>>, Exclude<i, InferString<def>>>;
	exclude(def: Def): FluentType<unknown>;
	onUndeclaredKey(behavior: "ignore" | "reject" | "delete"): FluentType<t, i>;
	onDeepUndeclaredKey(behavior: "ignore" | "reject" | "delete"): FluentType<t, i>;
}
interface PipeMethod<t, i> {
	<r>(fn: (data: t, ctx: NarrowContext) => r): FluentType<Exclude<r, OmpErrors>, i>;
	<r, ri>(schema: SchemaInference<r, ri>): FluentType<r, i>;
	(...steps: readonly unknown[]): FluentType<unknown, i>;
	readonly try: {
		<r>(fn: (data: t, ctx: NarrowContext) => r): FluentType<Exclude<r, OmpErrors>, i>;
		(...steps: readonly unknown[]): FluentType<unknown, i>;
	};
}
type InputObject<i> = i extends object ? i : object;
interface ObjectMethods<t extends object, i> {
	readonly props: readonly TypeProperty[];
	map(mapper: (property: TypeProperty) => TypeProperty | readonly TypeProperty[]): FluentType<Record<PropertyKey, unknown>>;
	keyof(): FluentType<Extract<keyof t, PropertyKey>, Extract<keyof InputObject<i>, PropertyKey>>;
	get<const path extends readonly PropertyKey[]>(...path: path): FluentType<unknown>;
	pick<const keys extends readonly (keyof t)[]>(...keys: keys): FluentType<Pick<t, keys[number]>, Pick<InputObject<i>, Extract<keys[number], keyof InputObject<i>>>>;
	omit<const keys extends readonly (keyof t)[]>(...keys: keys): FluentType<Omit<t, keys[number]>, Omit<InputObject<i>, Extract<keys[number], keyof InputObject<i>>>>;
	partial(): FluentType<Partial<t>, Partial<InputObject<i>>>;
	required(): FluentType<Required<t>, Required<InputObject<i>>>;
	merge<r, ri>(def: SchemaInference<r, ri>): FluentType<MergeTypes<t, r>, MergeTypes<i, ri>>;
	merge<const def extends Record<string, unknown>>(def: def): FluentType<MergeTypes<t, InferObjectLiteral<def>>, MergeTypes<i, InferObjectLiteralIn<def>>>;
	merge(def: Def): FluentType<unknown>;
}
type ObjectMethodsFor<t, i> = [
	t
] extends [
	never
] ? unknown : [
	t
] extends [
	readonly unknown[]
] ? unknown : [
	t
] extends [
	object
] ? ObjectMethods<t & object, i> : unknown;
type FluentType<t = unknown, i = t> = Type<t, i> & FluentMethods<t, i> & ObjectMethodsFor<t, i>;
type FnDefinition = Def | SchemaInference<unknown, unknown>;
type TypedFunction<parameters extends readonly unknown[] = readonly unknown[], returns = unknown, declaredReturns = returns> = ((...arguments_: parameters) => returns) & {
	readonly params: FluentType<parameters>;
	readonly returns: FluentType<declaredReturns>;
	readonly expression: string;
	readonly raw: (...arguments_: parameters) => returns;
};
type InferFnDefinition<definition> = definition extends SchemaInference<infer output, unknown> ? output : InferDef<definition>;
type InferFnParameters<definitions extends readonly unknown[], accumulator extends readonly unknown[] = [
]> = definitions extends readonly [
	infer head,
	...infer tail
] ? head extends ":" ? accumulator : InferFnParameters<tail, readonly [
	...accumulator,
	InferFnDefinition<head>
]> : accumulator;
type InferFnReturn<definitions extends readonly unknown[], inferred> = definitions extends readonly [
	...(readonly unknown[]),
	":",
	infer returns
] ? InferFnDefinition<returns> : inferred;
type DeclaredFnReturn<definitions extends readonly unknown[]> = definitions extends readonly [
	...(readonly unknown[]),
	":",
	infer returns
] ? InferFnDefinition<returns> : unknown;
type FnFactory<definitions extends readonly FnDefinition[]> = <result>(implementation: (...arguments_: InferFnParameters<definitions>) => InferFnReturn<definitions, result>) => TypedFunction<InferFnParameters<definitions>, InferFnReturn<definitions, result>, DeclaredFnReturn<definitions>>;
interface FnParser {
	<const definitions extends readonly FnDefinition[]>(...definitions: definitions): FnFactory<definitions>;
	raw<const definitions extends readonly FnDefinition[]>(...definitions: definitions): FnFactory<definitions>;
}
declare const Type: () => void;
interface ConfigureSelector {
	readonly kind?: string;
	readonly where?: (node: {
		readonly domain?: string;
		readonly kind: string;
	}) => boolean;
}
type Generic = (...arguments_: readonly unknown[]) => BaseType;
interface GenericArguments {
	readonly [name: string]: BaseType;
}
interface GenericBuilder {
	(definition: (arguments_: GenericArguments) => unknown, hkt?: unknown): Generic;
	(definition: unknown, hkt?: unknown): Generic;
}
declare function type<const definition>(parameters: `<${string}>`, definition: definition): Generic;
declare function type<const def>(def: def): FluentType<InferDef<def>, InferDefIn<def>>;
declare function type<input, output>(def: SchemaInference<input> | string, operator: "=>", morph: (data: input, ctx: NarrowContext) => output): FluentType<Exclude<output, OmpErrors>, input>;
declare function type<const input, const output>(def: input, operator: "|>", out: output): FluentType<InferDef<output>, InferDefIn<input>>;
declare function type<const expression extends readonly unknown[]>(...definition: expression): FluentType<InferDef<expression>, InferDefIn<expression>>;
interface ParsedStringKeyword<parsed> extends FluentType<string> {
	readonly parse: FluentType<parsed, string>;
}
interface PreformattedKeyword extends FluentType<string, string> {
	readonly preformatted: FluentType<string>;
}
interface Base64Keyword extends FluentType<string> {
	readonly url: FluentType<string>;
}
interface DateStringKeyword extends ParsedStringKeyword<Date> {
	readonly iso: ParsedStringKeyword<Date>;
	readonly epoch: ParsedStringKeyword<Date>;
}
interface IpKeyword extends FluentType<string> {
	readonly v4: FluentType<string>;
	readonly v6: FluentType<string>;
}
interface UuidKeyword extends FluentType<string> {
	readonly v1: FluentType<string>;
	readonly v2: FluentType<string>;
	readonly v3: FluentType<string>;
	readonly v4: FluentType<string>;
	readonly v5: FluentType<string>;
	readonly v6: FluentType<string>;
	readonly v7: FluentType<string>;
	readonly v8: FluentType<string>;
}
interface NormalizeKeyword extends PreformattedKeyword {
	readonly NFC: PreformattedKeyword;
	readonly NFD: PreformattedKeyword;
	readonly NFKC: PreformattedKeyword;
	readonly NFKD: PreformattedKeyword;
}
interface ParseKeyword {
	readonly number: FluentType<number, string>;
	readonly integer: FluentType<number, string>;
	readonly json: FluentType<unknown, string>;
	readonly date: FluentType<Date, string>;
	readonly url: FluentType<URL, string>;
	readonly boolean: FluentType<boolean, string>;
	readonly bigint: FluentType<bigint, string>;
}
interface StringKeyword extends FluentType<string> {
	readonly alpha: FluentType<string>;
	readonly alphanumeric: FluentType<string>;
	readonly base64: Base64Keyword;
	readonly capitalize: PreformattedKeyword;
	readonly creditCard: FluentType<string>;
	readonly date: DateStringKeyword;
	readonly digits: FluentType<string>;
	readonly email: FluentType<string>;
	readonly hex: FluentType<string>;
	readonly integer: ParsedStringKeyword<number>;
	readonly ip: IpKeyword;
	readonly json: ParsedStringKeyword<unknown>;
	readonly lower: PreformattedKeyword;
	readonly normalize: NormalizeKeyword;
	readonly numeric: ParsedStringKeyword<number>;
	readonly regex: FluentType<string>;
	readonly semver: FluentType<string>;
	readonly trim: PreformattedKeyword;
	readonly upper: PreformattedKeyword;
	readonly url: ParsedStringKeyword<URL>;
	readonly uuid: UuidKeyword;
}
interface NumberKeyword extends FluentType<number> {
	readonly integer: FluentType<number>;
}
type Constructed<ctor> = ctor extends abstract new (...args: never[]) => infer instance ? instance : never;
type MatchDefault<input = unknown, output = unknown> = "assert" | "never" | "reject" | ((input: input, ...args: readonly unknown[]) => output);
type MatchCaseOutput<cases> = {
	[key in keyof cases]: cases[key] extends (...args: never[]) => infer output ? output : never;
}[keyof cases];
type Matcher<input = unknown, output = unknown> = FluentType<output, input> & (<const value extends input>(value: value, ...args: readonly unknown[]) => output | OmpErrors);
interface MatchParser<input = unknown, output = never> {
	<const cases extends Record<PropertyKey, unknown>>(cases: cases): MatchParser<input, output | MatchCaseOutput<cases>> | Matcher<input, output | MatchCaseOutput<cases>>;
	case<const definition, result>(definition: definition, resolver: (value: InferDef<definition>, ...args: readonly unknown[]) => result): MatchParser<input, output | result>;
	match<const cases extends Record<PropertyKey, unknown>>(cases: cases): MatchParser<input, output | MatchCaseOutput<cases>> | Matcher<input, output | MatchCaseOutput<cases>>;
	default<const result>(fallback: MatchDefault<input, result>): Matcher<input, output | result>;
	at<const key extends PropertyKey>(key: key): MatchParser<input, output>;
	at<const key extends PropertyKey, cases extends Record<PropertyKey, unknown>>(key: key, cases: cases): MatchParser<input, output | MatchCaseOutput<cases>> | Matcher<input, output | MatchCaseOutput<cases>>;
	strings<const cases extends Record<PropertyKey, unknown>>(cases: cases): MatchParser<input, output | MatchCaseOutput<cases>> | Matcher<input, output | MatchCaseOutput<cases>>;
	in<narrowed>(): MatchParser<narrowed, output>;
	in<const definition>(definition: definition): MatchParser<InferDef<definition>, output>;
}
interface DeclaredParser<declared> {
	type<const definition>(definition: definition): FluentType<declared, InferDefIn<definition>>;
}
declare namespace type {
	/** Error aggregate returned by failed validations (`result instanceof type.errors`). */
	export const errors: typeof OmpErrors;
	export type errors = OmpErrors;
	/** Build a union from zero or more definitions. */
	export function or<const definitions extends readonly unknown[]>(...definitions: definitions): FluentType<NaryOrOutput<definitions>, NaryOrInput<definitions>>;
	/** Build an array schema from an element definition. */
	export function array<const definition>(definition: definition): FluentType<InferDef<definition>[], InferDefIn<definition>[]>;
	/** Build a union from a runtime array of definitions. */
	export function union<const definitions extends readonly unknown[]>(definitions: definitions): FluentType<NaryOrOutput<definitions>, NaryOrInput<definitions>>;
	/** Build a tuple schema from a runtime array of definitions. */
	export function tuple<const definitions extends readonly unknown[]>(definitions: definitions): FluentType<InferDef<definitions>, InferDefIn<definitions>>;
	/** Build an open record schema from key and value definitions. */
	export function record<const key, const value>(key: key, value: value): FluentType<Record<Extract<InferDef<key>, PropertyKey>, InferDef<value>>, Record<Extract<InferDefIn<key>, PropertyKey>, InferDefIn<value>>>;
	/** Build an intersection from zero or more definitions. */
	export function and<const definitions extends readonly unknown[]>(...definitions: definitions): FluentType<NaryAndOutput<definitions>, NaryAndInput<definitions>>;
	/** Right-biased object merge over zero or more definitions. */
	export function merge<const definitions extends readonly unknown[]>(...definitions: definitions): FluentType<NaryMergeOutput<definitions>, NaryMergeInput<definitions>>;
	/** Compose Types, definitions, and morph callbacks from left to right. */
	export function pipe<const definitions extends readonly unknown[]>(...definitions: definitions): FluentType<NaryPipeOutput<definitions>, NaryPipeInput<definitions>>;
	/** String validator and its refinement/morph keyword module. */
	export const string: StringKeyword;
	/** Runtime parser keyword family. */
	export const parse: ParseKeyword;
	/** Number validator with integer refinement. */
	export const number: NumberKeyword;
	/** Schema-valued key representing any non-negative integer array index. */
	export const arrayIndex: FluentType<string, string>;
	/** Boolean validator. */
	export const boolean: FluentType<boolean, boolean>;
	/** Bigint validator. */
	export const bigint: FluentType<bigint, bigint>;
	/** Symbol validator. */
	export const symbol: FluentType<symbol, symbol>;
	/** Non-null object validator. */
	export const object: FluentType<object, object>;
	/** Unknown validator. */
	export const unknown: FluentType<unknown, unknown>;
	/** Alias of the unknown validator. */
	export const any: FluentType<unknown, unknown>;
	/** Validator that rejects every value. */
	export const never: FluentType<never, never>;
	/** ArkType's built-in keyword namespace, including invokable utility generics. */
	export const keywords: {
		number: {
			integer: FluentType<number, number>;
		};
		Map: FluentType<Map<unknown, unknown>, Map<unknown, unknown>>;
		Set: FluentType<Set<unknown>, Set<unknown>>;
		RegExp: FluentType<RegExp, RegExp>;
		File: FluentType<File, File>;
		Error: FluentType<Error, Error>;
		Function: FluentType<Function, Function>;
		Array: {
			liftFrom<const definition>(definition: definition): FluentType<InferDef<definition>[], InferDefIn<definition> | InferDefIn<definition>[]>;
		};
		Record<const key, const value>(key: key, value: value): FluentType<Record<Extract<InferDef<key>, PropertyKey>, InferDef<value>>, Record<Extract<InferDefIn<key>, PropertyKey>, InferDefIn<value>>>;
		Partial<const definition>(definition: definition): FluentType<Partial<InferDef<definition>>, Partial<InputObject<InferDefIn<definition>>>>;
		Required<const definition>(definition: definition): FluentType<Required<InferDef<definition>>, Required<InputObject<InferDefIn<definition>>>>;
		Pick<const definition, const keys extends readonly PropertyKey[]>(definition: definition, ...keys: keys): FluentType<Pick<InferDef<definition>, Extract<keys[number], keyof InferDef<definition>>>, Pick<InputObject<InferDefIn<definition>>, Extract<keys[number], keyof InputObject<InferDefIn<definition>>>>>;
		Omit<const definition, const keys extends readonly PropertyKey[]>(definition: definition, ...keys: keys): FluentType<Omit<InferDef<definition>, Extract<keys[number], keyof InferDef<definition>>>, Omit<InputObject<InferDefIn<definition>>, Extract<keys[number], keyof InputObject<InferDefIn<definition>>>>>;
		Merge<const left, const right>(left: left, right: right): FluentType<MergeTypes<InferDef<left>, InferDef<right>>, MergeTypes<InferDefIn<left>, InferDefIn<right>>>;
		object: {
			json: FluentType<unknown, unknown>;
		};
		unknown: {
			any: FluentType<unknown, unknown>;
		};
	};
	/** Date instance validator. */
	export const Date: FluentType<Date, Date>;
	/** Validate instances of `ctor`. */
	export function instanceOf<const ctor extends Constructor>(ctor: ctor): FluentType<Constructed<ctor>>;
	/** Validate one exact unit value. */
	export function unit<const value>(value: value): FluentType<value>;
	/** Union of literal values from a runtime array. */
	export function enumerated<const values extends readonly unknown[]>(...values: values): FluentType<values[number]>;
	/** Build a literal union from a runtime array. */
	export function enumeration<const values extends readonly unknown[]>(values: values): FluentType<values[number]>;
	/** Enumerate an enum-like object's forward values, excluding numeric reverse mappings. */
	export function valueOf<const values extends Record<PropertyKey, unknown>>(values: values): FluentType<values[keyof values]>;
	/** Fluent first-match dispatcher, also exported as standalone `match`. */
	export const match: MatchParser;
	/** Preserve a definition's literal type while authoring reusable modules. */
	export function define<const definition>(definition: definition): definition;
	/** Build a function whose arguments and optional declared return are validated. */
	export const fn: FnParser;
	/** Fix an externally declared static type while retaining runtime validation. */
	export const declare: <declared, _options = {}>() => DeclaredParser<declared>;
	/** Build a lazy named scope from aliases and recursive definitions. */
	export function scope(aliases: Record<string, unknown>, options?: ScopeOptions): TypeScope;
	/** Compile a named schema module whose definitions may reference each other. */
	export function module<const definitions extends Record<string, unknown>>(definitions: definitions, options?: ScopeOptions): {
		[name in keyof definitions]: Type<InferDef<definitions[name]>, InferDefIn<definitions[name]>>;
	};
	type GenericParameterSpec = string | readonly [
		name: string,
		constraint: unknown
	];
	/** Build a generic directly from an angle-bracket declaration. */
	export function generic<const definition>(parameters: `<${string}>`, definition: definition): Generic;
	/** Build a curried generic from named, optionally constrained parameters. */
	export function generic(...parameters: readonly GenericParameterSpec[]): GenericBuilder;
	/** Untyped builder for runtime-assembled definitions. */
	export function raw(def: unknown): BaseType;
	/**
	 * Return a validation-only schema that emits `json` verbatim — even when
	 * embedded in an object, array, or union.
	 *
	 * A `.toJsonSchema()` method override cannot survive nesting: a parent schema
	 * emits each child's IR directly and never calls the child's method, so the
	 * override silently disappears from the wire schema. This stores the override
	 * on the IR instead.
	 *
	 * # Errors
	 *
	 * Throws when `schema` has a default or output-changing morph/pipe. A refine
	 * can preserve validation and the input value, but silently discarding a
	 * transformed output would violate the returned {@link Type}.
	 */
	export function withJsonSchema<t, i = t>(schema: Type<t, i>, json: Record<string, unknown>): Type<t, i>;
	export {};
}
interface ScopeOptions {
	jitless?: boolean;
	clone?: false | ((input: unknown) => unknown);
	divisor?: SchemaConfig;
}
type ScopedBuilder = (<const definition>(definition: definition) => FluentType<InferDef<definition>, InferDefIn<definition>>) & typeof type;
interface TypeScope {
	readonly type: ScopedBuilder;
	readonly match: MatchParser;
	readonly json: Record<string, unknown>;
	define<const definition>(definition: definition): definition;
	resolve(name: string): BaseType;
	import(...names: readonly string[]): Record<string, unknown>;
	export(...names: readonly string[]): Record<string, BaseType>;
}
type BaseType = FluentType<unknown, unknown>;
declare const enum Effort {
	Minimal = "minimal",
	Low = "low",
	Medium = "medium",
	High = "high",
	XHigh = "xhigh",
	Max = "max"
}
interface ModelIdentity {
	/** Vendor lineage id, `"unknown"` when unclassified. */
	class: string;
	/** Product family within the class, when classified. */
	family?: string;
	/** Canonical `major.minor.patch`, when extracted. */
	revision?: string;
	/** Effort tier collapsed out of the id, when the id was an effort variant. */
	effort?: Effort | "off";
	/** Whether the id carried a thinking-variant suffix. */
	thinkingVariant?: boolean;
	/** Canonical logical id when it differs from the wire id. */
	logicalId?: string;
}
interface DatedCostCard extends Partial<TokenCost> {
	effectiveFrom: number;
}
declare class PipeStreams {
	constructor();
	/** The run's retained streams, oldest first. */
	list(): Array<PipeStreamInfo>;
	/**
	 * Retained bytes of stream `id` from stream offset `from` on (all of them
	 * without it), or `null` for a stream the run never opened or no longer
	 * keeps.
	 */
	read(id: number, from?: number | undefined | null): PipeStreamChunk | null;
}
interface PipeStreamChunk {
	/**
	 * Stream offset of `data`'s first byte; past 0 once older bytes were
	 * dropped.
	 */
	start: number;
	/** Stream offset just past `data`: the next read's `from`. */
	end: number;
	/** The bytes `start..end`. */
	data: Buffer;
	/** Whether bytes may still arrive. */
	open: boolean;
}
interface PipeStreamInfo {
	/** Stream number, from 1. */
	id: number;
	/** Source text of the stage writing into the pipe. */
	producer: string;
	/** Source text of the stage reading from it. */
	consumer: string;
	/** Bytes written so far, including those no longer retained. */
	total: number;
	/** Whether bytes may still arrive. */
	open: boolean;
}
type ModelResolutionSource = "bundled" | "cache" | "models.dev" | "provider";
type KnownApi = "openai-completions" | "openai-responses" | "openrouter" | "openai-codex-responses" | "azure-openai-responses" | "anthropic-messages" | "bedrock-converse-stream" | "google-generative-ai" | "google-gemini-cli" | "google-vertex" | "ollama-chat" | "cursor-agent" | "factory-droid-agent" | "gitlab-duo-agent" | "devin-agent" | "apple-foundation-models";
type Api = KnownApi | (string & {});
declare const MODEL_KINDS: readonly [
	"chat",
	"tiny",
	"image",
	"tts",
	"stt",
	"search",
	"judge",
	"embedding",
	"rerank",
	"video"
];
type ModelKind = (typeof MODEL_KINDS)[number];
type WebSearchGrounding = "gemini" | "anthropic" | "codex" | "xai" | "openrouter" | "openai";
type ThinkingControlMode = "effort" | "budget" | "google-level" | "anthropic-adaptive" | "anthropic-budget-effort";
interface ThinkingConfig {
	/** Provider-specific transport used to encode the selected effort. */
	mode: ThinkingControlMode;
	/**
	 * Supported user-facing efforts, ordered least → most intensive. Never
	 * empty: a reasoning model without a controllable effort surface carries
	 * `thinking: undefined` instead of an empty list.
	 */
	efforts: readonly Effort[];
	/** Optional default effort applied when this model is selected. Falls back to global default if absent. */
	defaultLevel?: Effort;
	/**
	 * Effort → provider wire-value remap, baked at build time. Identity for
	 * efforts the map omits. Used by Anthropic adaptive thinking, OpenAI-
	 * compatible `reasoning_effort`, and Responses-style reasoning params.
	 */
	effortMap?: Partial<Record<Effort, string>>;
	/**
	 * Adaptive thinking accepts the `display` field (Opus 4.7+, Fable/Mythos
	 * 5). Also implies native interleaved thinking — no beta header needed.
	 */
	supportsDisplay?: boolean;
	/**
	 * Thinking signatures bind each block to its preceding conversation prefix.
	 * Requests that rewrite the prefix must opt into the provider's drop/error control.
	 */
	prefixBinding?: boolean;
	/**
	 * Per-effort upstream wire-id routing for collapsed effort-tier variants
	 * (`compat/collapse.ts`). Keyed by pi effort; `"off"` applies when
	 * thinking is disabled. Missing keys fall back to `requestModelId ?? id`.
	 */
	effortRouting?: Readonly<Partial<Record<Effort | "off", string>>>;
	/**
	 * Per-effort thinking budget in tokens, baked at build time for collapsed
	 * variants whose upstream expects an explicit `thinkingBudget` instead of a
	 * value derived from the generic ladder (Antigravity Cloud Code Assist
	 * gemini-3.x). Request mapping prefers caller `thinkingBudgets`, then this
	 * map, then the provider default ladder. Only meaningful for `mode: "budget"`.
	 */
	effortBudgets?: Readonly<Partial<Record<Effort, number>>>;
	/**
	 * When true, a thinking-off request MUST explicitly suppress thinking on
	 * the wire (google-level: `thinkingLevel: "MINIMAL"` + `includeThoughts:
	 * false`; budget: `thinkingBudget: 0`) instead of omitting thinkingConfig —
	 * Cloud Code Assist re-applies the per-id baked server default when the
	 * config is absent.
	 */
	suppressWhenOff?: boolean;
	/**
	 * Reasoning is mandatory upstream: the endpoint rejects disabled or
	 * omitted thinking (e.g. OpenRouter Gemini 3.x — "Reasoning is mandatory
	 * for this endpoint and cannot be disabled"). Request mapping clamps
	 * thinking-off to the lowest supported effort unless `suppressWhenOff`
	 * provides an explicit wire off-path.
	 */
	requiresEffort?: boolean;
	/**
	 * The catalog has no evidence this model reasons (unclassified id, no
	 * capability metadata). The ladder is offered for the user to opt into:
	 * sessions leave thinking unset until the user picks an effort, and a
	 * request without an effort goes out as non-reasoning (see
	 * `withoutUnverifiedThinking`).
	 */
	unverified?: boolean;
}
type Provider = string;
interface Usage {
	/** Non-cached conversation input tokens (matches the bucket the provider bills as new input). */
	input: number;
	/** Total conversation output tokens for the turn, including thinking, assistant text, and tool-call argument tokens. */
	output: number;
	/** Conversation tokens read from the prompt cache. */
	cacheRead: number;
	/** Conversation tokens written to the prompt cache (cache creation). */
	cacheWrite: number;
	/** Sum of input + output + cacheRead + cacheWrite plus provider-side orchestration tokens when reported. */
	totalTokens: number;
	/** Provider-reported occupied context tokens when the value is authoritative but not a billable input/output bucket. */
	contextTokens?: number;
	/** Provider-side orchestration tokens, billed but not part of the conversation prompt/cache buckets. */
	orchestration?: {
		/** Non-cached orchestration input tokens. */
		input?: number;
		/** Orchestration tokens read from provider-side cache. */
		cacheRead?: number;
		/** Orchestration output tokens. */
		output?: number;
	};
	/** Copilot premium-request counter, when applicable. */
	premiumRequests?: number;
	/**
	 * Reasoning/thinking tokens included in `output`, when the provider reports them
	 * (OpenAI `output_tokens_details.reasoning_tokens`, Google `thoughtsTokenCount`).
	 * Always a subset of `output` — non-reasoning output is `output - reasoningTokens`.
	 *
	 * Providers that don't expose this leave it undefined rather than guessing;
	 * `undefined` means unknown, NOT zero.
	 */
	reasoningTokens?: number;
	/**
	 * Cache-write TTL breakdown (Anthropic only). When set, the components sum to
	 * `cacheWrite`. Absent providers do not populate this.
	 */
	cttl?: {
		ephemeral5m?: number;
		ephemeral1h?: number;
	};
	/**
	 * Server-side tool invocations made during this turn (Anthropic web_search /
	 * web_fetch, OpenAI built-in tools when reported). Counts requests, not tokens.
	 */
	server?: {
		webSearch?: number;
		webFetch?: number;
	};
	/** Provider-reported credit meter values for credit-based billing APIs. */
	credits?: {
		/** Uncommitted per-response credit cost, when reported. */
		cost?: number;
		/** Final committed credit cost, when reported. */
		committedCost?: number;
		/** Final committed ACU cost, when reported. */
		acuCost?: number;
	};
	cost: {
		input: number;
		output: number;
		cacheRead: number;
		cacheWrite: number;
		total: number;
	};
}
type OpenAIReasoningFormat = "openai" | "openrouter" | "zai" | "kimi" | "qwen" | "qwen-chat-template" | "chat-template";
type OpenAIReasoningDisableMode = "omit" | "lowest-effort" | "none-effort" | "openrouter-enabled-false" | "cline-enabled-false" | "venice-disable-thinking" | "zai-thinking-disabled" | "qwen-enable-thinking-false" | "qwen-template-false" | "chat-template-thinking-false";
type OpenAIStreamMarkupHealingPattern = "kimi" | "dsml" | "qwen" | "thinking";
interface OpenAICompat {
	/** Whether the provider supports the `store` field. Default: auto-detected from URL. */
	supportsStore?: boolean;
	/** Whether the provider supports the `developer` role (vs `system`). Default: auto-detected from URL. */
	supportsDeveloperRole?: boolean;
	/**
	 * Whether the provider's chat-completions endpoint accepts multiple
	 * leading `system`/`developer` messages. When false, ordered system
	 * prompts are coalesced into a single message joined by `\n\n` so
	 * strict chat templates (e.g. Qwen-served via vLLM, MiniMax) accept
	 * the request. Default: detected per provider/baseUrl. Canonical
	 * OpenAI/Azure/OpenRouter/Cerebras/Together/Fireworks/Groq/DeepSeek/
	 * Mistral/xAI/Z.ai/GitHub Copilot/Zenmux are treated as `true`;
	 * unknown or strict-template hosts default to `false`. Setting this
	 * to `true` preserves separate blocks, which is preferred for
	 * KV-cache reuse when the trailing prompt changes between calls.
	 */
	supportsMultipleSystemMessages?: boolean;
	/** Whether the provider supports `reasoning_effort`. Default: auto-detected from URL. */
	supportsReasoningEffort?: boolean;
	/** Do not infer a thinking dial when discovery supplies no explicit thinking configuration. */
	trustExplicitThinkingOnly?: boolean;
	/** Optional mapping from pi-ai reasoning levels to provider/model-specific `reasoning_effort` values. */
	reasoningEffortMap?: Partial<Record<Effort, string>>;
	/** Whether the provider supports `stream_options: { include_usage: true }` for token usage in streaming responses. Default: true. */
	supportsUsageInStreaming?: boolean;
	/** Which field to use for max tokens. Default: auto-detected from URL. */
	maxTokensField?: "max_completion_tokens" | "max_tokens";
	/** Whether tool results require the `name` field. Default: auto-detected from URL. */
	requiresToolResultName?: boolean;
	/** Whether a user message after tool results requires an assistant message in between. Default: auto-detected from URL. */
	requiresAssistantAfterToolResult?: boolean;
	/** Whether thinking blocks must be converted to text blocks with <thinking> delimiters. Default: auto-detected from URL. */
	requiresThinkingAsText?: boolean;
	/** Whether tool call IDs must be normalized to Mistral format (exactly 9 alphanumeric chars). Default: auto-detected from URL. */
	requiresMistralToolIds?: boolean;
	/** Format for reasoning/thinking parameter. `"kimi"` uses `thinking: { type, effort }`; other values select their provider-native reasoning fields. Default: `"openai"`. */
	thinkingFormat?: OpenAIReasoningFormat;
	/** Kimi Code transport selected by live per-model protocol metadata. User settings take precedence. */
	kimiApiFormat?: "openai" | "anthropic";
	/** Request-time disable encoding for the selected reasoning/thinking format. Default: derived from `thinkingFormat`. */
	reasoningDisableMode?: OpenAIReasoningDisableMode;
	/** Whether the provider rejects `reasoning.effort`/`reasoning_effort` even when the model reasons natively. Default: false unless reasoning effort is unsupported. */
	omitReasoningEffort?: boolean;
	/** Whether Responses requests should ask for encrypted reasoning replay items. Default: true. */
	includeEncryptedReasoning?: boolean;
	/** Whether replayed Responses history should strip native `type: "reasoning"` items before request encoding. Default: false. */
	filterReasoningHistory?: boolean;
	/** Optional `thinking.keep` value for Z.ai/Moonshot-style thinking params. Set false to suppress auto-detected keep. Default: auto-detected. */
	thinkingKeep?: "all" | false;
	/** Which reasoning content field to emit on assistant messages. Default: auto-detected. */
	reasoningContentField?: "reasoning_content" | "reasoning" | "reasoning_text";
	/** Decode Mistral thinking parts in streamed content and replay them as typed assistant content. */
	mistralReasoningContentParts?: boolean;
	/** Whether assistant tool-call messages must include reasoning content. Default: false. */
	requiresReasoningContentForToolCalls?: boolean;
	/** Whether all assistant messages must include reasoning content. Default: false. */
	requiresReasoningContentForAllAssistantTurns?: boolean;
	/** Whether the provider accepts a synthetic placeholder (e.g. ".") for missing reasoning_content on tool-call turns. Default: true. Set to false for providers like DeepSeek that validate the exact reasoning_content value. */
	allowsSyntheticReasoningContentForToolCalls?: boolean;
	/**
	 * Value emitted for the reasoning field on tool-call turns when the provider
	 * requires it (`requiresReasoningContentForToolCalls`) but no reasoning was
	 * captured. Default: "". Some upstreams validate the exact value — the droid
	 * proxy's DeepSeek family requires a single space.
	 */
	syntheticReasoningContentFallback?: string;
	/**
	 * Replay preserved thinking blocks as `reasoning_content` (or the configured
	 * `reasoningContentField`) on EVERY assistant turn that carried reasoning,
	 * regardless of whether the upstream provider validates the field. Local
	 * llama.cpp-style servers (llama.cpp, LM Studio, vLLM, sglang, Ollama in
	 * openai-completions mode) re-tokenize the full chat-template prompt every
	 * request; Qwen3 / DeepSeek-R1 / GLM templates reconstruct the `<think>`
	 * block from `reasoning_content`. Dropping the field re-renders the
	 * assistant turn without `<think>`, diverging from the slot's KV cache state
	 * and forcing full prompt re-processing (#3528). Default: auto-detected
	 * (loopback/private baseUrl or local provider id with thinking-enabled
	 * models).
	 */
	replayReasoningContent?: boolean;
	/**
	 * Send `preserve_thinking: true` so the Qwen3.6+ chat template renders
	 * `<think>...</think>` markup for EVERY assistant turn (not just turns
	 * after the last user message). Without it, the template strips the think
	 * block from older assistant turns:
	 *
	 * ```jinja
	 * {%- if (preserve_thinking is defined and preserve_thinking is true)
	 *        or (loop.index0 > ns.last_query_index) %}
	 *   <|im_start|>assistant\n<think>\n{rc}\n</think>\n\n{content}
	 * {%- else %}
	 *   <|im_start|>assistant\n{content}
	 * ```
	 *
	 * The cache from the original generation has `<think>...</think>` tokens,
	 * so once a new user message arrives the prior assistant turns become
	 * "older" and the stripped re-render diverges — full prompt re-processing
	 * on SWA models (#3541). Default: auto-detected (Qwen thinking format on
	 * a local llama.cpp-style backend, paired with `replayReasoningContent`).
	 * Non-Qwen templates ignore the flag, so the auto-detection is safe.
	 */
	qwenPreserveThinking?: boolean;
	/**
	 * Route the requested thinking effort onto the Qwen 3.8+ chat template's
	 * `reasoning_effort` kwarg (`low`/`medium`/`xhigh`; template default
	 * `xhigh`). Emitted inside `chat_template_kwargs` for both Qwen dialects
	 * (plus the top-level field on the `qwen` dialect, which newer llama.cpp
	 * builds map natively). Without it the qwen dialects only toggle
	 * `enable_thinking` and the template always thinks at its `xhigh` default.
	 * Default: auto-detected (Qwen 3.8+ id on a local llama.cpp-style backend).
	 */
	qwenTemplateReasoningEffort?: boolean;
	/** Whether assistant tool-call messages must include non-empty content. Default: false. */
	requiresAssistantContentForToolCalls?: boolean;
	/** Whether the provider supports the `tool_choice` parameter. Default: true. */
	supportsToolChoice?: boolean;
	/**
	 * Whether forced `tool_choice` values (`"required"` or named tools) are accepted.
	 * When false, request builders keep tools available but downgrade forced choices
	 * to provider-default auto selection. Default: true.
	 */
	supportsForcedToolChoice?: boolean;
	/**
	 * Whether the chat-completions endpoint accepts the object form that pins one
	 * named function (`{ type: "function", function: { name } }`). Some
	 * OpenAI-compatible hosts such as llama.cpp only accept string
	 * `tool_choice` values; request builders downgrade a named force to
	 * `"required"` when this is false. Default: true.
	 */
	supportsNamedToolChoice?: boolean;
	/**
	 * Drop reasoning fields (`reasoning_effort`, OpenRouter `reasoning`) for
	 * the request when `tool_choice` forces a tool call. Mirrors the Anthropic
	 * `disableThinkingIfToolChoiceForced` rule for backends like Kimi that
	 * 400 with `tool_choice 'specified' is incompatible with thinking
	 * enabled` whenever both are present. Default: auto-detected (Kimi).
	 */
	disableReasoningOnForcedToolChoice?: boolean;
	/**
	 * Drop reasoning fields (`reasoning_effort`, OpenRouter `reasoning`) for
	 * any request that sends `tool_choice`. Use for providers/models that accept
	 * tools and `tool_choice`, but reject `tool_choice` while thinking is enabled.
	 * Default: auto-detected (DeepSeek reasoning models).
	 */
	disableReasoningOnToolChoice?: boolean;
	/**
	 * Disable reasoning whenever the request advertises function tools.
	 * Use for model surfaces that reject every tools-plus-reasoning combination.
	 * Default: false.
	 */
	disableReasoningWithTools?: boolean;
	/** OpenRouter-specific routing preferences. Only used when baseUrl points to OpenRouter. */
	openRouterRouting?: OpenRouterRouting;
	/** Vercel AI Gateway routing preferences. Only used when baseUrl points to Vercel AI Gateway. */
	vercelGatewayRouting?: VercelGatewayRouting;
	/**
	 * Provider-specific wire model-id transform applied to the base id, overriding
	 * the provider-detected default. Discovery layers set this per model when the
	 * upstream roster declares the id's namespace (e.g. ClinePass free-tier models
	 * arrive pre-namespaced and use `"raw"`), so wire form follows data rather
	 * than id shape.
	 */
	wireModelIdMode?: "raw" | "cline-pass" | "firepass" | "fireworks" | "openrouter";
	/** Extra fields to include in request body (e.g. gateway routing hints for OpenClaw-style proxies). */
	extraBody?: Record<string, unknown>;
	/** Request-session header that should mirror the normalized prompt-cache key. Default: unset. */
	promptCacheSessionHeader?: "x-grok-conv-id" | "x-session-id";
	/** Request header carrying the conversation session id, independent of prompt caching. Default: unset. */
	sessionHeader?: "x-litellm-session-id";
	/** Whether chat-completions payloads should include provider-specific prompt-cache markers. */
	cacheControlFormat?: "anthropic" | undefined;
	/**
	 * Whether this endpoint/model accepts OpenAI's explicit
	 * `prompt_cache_breakpoint` content markers and `prompt_cache_options`.
	 * Defaults to the exact first-party model generation allowlist.
	 */
	supportsPromptCacheBreakpoints?: boolean;
	/** The only currently supported minimum lifetime for explicit OpenAI cache breakpoints. */
	promptCacheBreakpointTtl?: "30m";
	/** Whether the provider supports the `strict` field in tool definitions. Default: auto-detected per provider/baseUrl (conservative for unknown providers). */
	supportsStrictMode?: boolean;
	/**
	 * Tool-schema dialect the endpoint validates `tools.function.parameters`
	 * against.
	 *
	 * `"moonshot-mfjs"` triggers Moonshot Flavored JSON Schema normalization
	 * (collapse `const`→`enum`, infer `type` on bare enums, strip unsupported
	 * validators/`prefixItems`) because Moonshot/Kimi native hosts reject
	 * standard JSON Schema constructs with HTTP 400.
	 *
	 * `"grammar"` triggers grammar-sampler normalization (widen bare boolean
	 * `true`/`{}` subschemas in genuine subschema slots into a value-accepting
	 * union of primitives) because grammar-constrained backends (llama.cpp, LM
	 * Studio, vLLM) build a GBNF grammar from the JSON Schema and 400 with
	 * `Unrecognized schema: true` on a bare boolean subschema (issue #5914).
	 * Boolean `additionalProperties`/`unevaluatedProperties` are preserved — the
	 * grammar converter reads them as closed/open-object semantics, and
	 * `additionalProperties: false` pins the strict object shape.
	 *
	 * Default: auto-detected — `"moonshot-mfjs"` on Moonshot native hosts
	 * (api.moonshot.ai / api.kimi.com) and Kimi-family model ids on any host,
	 * since proxies (OpenRouter, custom gateways) forward schemas to Moonshot
	 * verbatim; `"grammar"` on local OpenAI-compatible backends. Set `"none"`
	 * to opt a host out.
	 */
	toolSchemaFlavor?: "moonshot-mfjs" | "grammar" | "none";
	/**
	 * Stream-watchdog first-event timeout in ms.
	 * Set to `0` to allow unbounded prompt processing. Default: auto-detected
	 * (disabled for local OpenAI-compatible backends).
	 */
	streamFirstEventTimeoutMs?: number;
	/**
	 * Stream-watchdog idle-timeout floor in ms for slow reasoning hosts.
	 * Default: auto-detected (GLM coding-plan hosts, direct DeepSeek reasoning).
	 */
	streamIdleTimeoutMs?: number;
	/** Whether the host honors `prompt_cache_retention: "24h"` on the Responses API. Default: auto-detected (api.openai.com). */
	supportsLongPromptCacheRetention?: boolean;
	/** Whether tool schemas must be sent either all strict or all non-strict. Undefined keeps the existing per-tool mixed behavior. */
	toolStrictMode?: "all_strict" | "none";
	/** Whether request shaping may send reasoning params at all. Default: auto-detected (disabled for GitHub Copilot chat-completions). */
	supportsReasoningParams?: boolean;
	/** Whether Responses requests may include `reasoning.summary`. Default: true except on known incompatible hosts. */
	supportsReasoningSummary?: boolean;
	/** Whether to chain OpenAI Responses turns using stored response ids. Unset uses the official-endpoint default; env and call options take precedence. */
	statefulResponses?: boolean;
	/**
	 * Whether the endpoint accepts explicit sampling parameters (`temperature`,
	 * `top_p`, `top_k`, `min_p`, penalties). OpenAI proprietary reasoning models
	 * (o-series, gpt-5+) reject them with `400 Unsupported parameter:
	 * 'temperature' is not supported with this model` on every serving host
	 * (official, Azure, GitHub Copilot). When unset, auto-detected from the
	 * model id. Default: true. Issue #5606.
	 */
	supportsSamplingParams?: boolean;
	/**
	 * Whether presence/frequency penalties and stop sequences may be sent.
	 * First-party xAI `/v1/responses` rejects penalty fields for every model.
	 * xAI reasoning models also reject them (and `stop`) on chat completions.
	 * When unset, auto-detected. Default: true.
	 */
	supportsPenaltyAndStopParams?: boolean;
	/** Always send a max-token field when the caller did not provide one. Default: auto-detected (Kimi-family models derive TPM limits from max_tokens). */
	alwaysSendMaxTokens?: boolean;
	/** Whether Responses-API tool-call/result history must be strictly paired. Default: auto-detected (Azure OpenAI, GitHub Copilot). */
	strictResponsesPairing?: boolean;
	/** Whether the Responses API accepts the `detail: "original"` image hint. Default: true for OpenAI, Azure OpenAI, and Codex; false for other hosts. Explicit overrides win. */
	supportsImageDetailOriginal?: boolean;
	/**
	 * Whether the Responses endpoint accepts `configuration_update` input items
	 * that change `reasoning.effort` mid-conversation while the request-level
	 * effort stays pinned for prompt caching (GPT-6 family). Default:
	 * rule-detected (`true` for GPT-6 models on any host, `false` otherwise).
	 * Set `false` for custom `openai-responses` / `openai-codex-responses`
	 * endpoints that reject the item type with HTTP 400; effort changes are then
	 * sent as the top-level `reasoning.effort`.
	 */
	supportsConfigurationUpdate?: boolean;
	/**
	 * Whether the Responses WebSocket accepts `response.steer`, which queues user
	 * input into the in-flight response (GPT-6 family). Default: rule-detected.
	 * Set `false` for proxies that reject the event; steering then waits for the
	 * next request boundary.
	 */
	supportsSteering?: boolean;
	/** Whether streamed reasoning deltas for the same field may repeat the full cumulative text snapshot. Default: false. */
	reasoningDeltasMayBeCumulative?: boolean;
	/** Strip leaked DeepSeek chat-template special tokens from visible content deltas. Default: auto-detected. */
	stripDeepseekSpecialTokens?: boolean;
	/** Drop assistant thinking blocks from replayed chat-completions history. Default: false. */
	stripThinkingHistory?: boolean;
	/** Heal leaked chat-template/tool-call/thinking markup from visible content deltas. Default: auto-detected. */
	streamMarkupHealingPattern?: OpenAIStreamMarkupHealingPattern;
	/** Whether this wire may revise already-streamed text (`stream-revision` axis). Unassigned: append-only. */
	streamRevision?: "none" | "possible";
	/** Treat an empty length-finished stream as a context-window error. Default: auto-detected. */
	emptyLengthFinishIsContextError?: boolean;
	/** Normalize tool call ids to OpenAI's 40-character limit. Default: auto-detected. */
	usesOpenAIToolCallIdLimit?: boolean;
	/**
	 * Compat deltas applied when a request actually engages thinking mode
	 * (reasoning requested and not disabled, model reasoning-capable, and not
	 * suppressed by a forced tool choice). `buildModel` materializes the full
	 * alternate view as `compat.whenThinking`; handlers pointer-swap, never
	 * spread. Default: auto-detected (OpenCode gateways, #1071/#1484).
	 */
	whenThinking?: Partial<Omit<OpenAICompat, "whenThinking">>;
	/** Kimi K3 drives reasoning through native `reasoning_effort` on Moonshot-native hosts. */
	nativeKimiK3Reasoning?: boolean;
	/** GLM-5.2 accepts Z.ai's `reasoning_effort` dialect alongside binary thinking. */
	zaiReasoningEffortDialect?: boolean;
	/** Clamp the requested max output tokens to the model's advertised ceiling. */
	clampOutputToModelMax?: boolean;
	/** Strip image inputs before encoding (text-only serving of a multimodal id). */
	stripImageInput?: boolean;
}
interface AnthropicCompat {
	/** Whether thinking requests may include `context_management` and its beta header. Default: true. */
	supportsContextManagement?: boolean;
	/**
	 * Whether the model and host support Anthropic on-demand compaction
	 * (`compact-2026-09-04` requests and signed replay). Enabled on Opus 4.6+,
	 * Sonnet 4.6+, Fable/Mythos 5+ on supported hosts. Default: false.
	 */
	supportsServerCompaction?: boolean;
	/**
	 * Whether the model supports message threads (`message-threads-2026-08-12`):
	 * later turns continue a server-held conversation and send only new
	 * messages. Enabled on Claude 4.5+ for the first-party provider; the
	 * transport still requires the official endpoint unless the caller opts in.
	 * Default: false.
	 */
	supportsMessageThreads?: boolean;
	/**
	 * Whether the model is served by the first-party Anthropic provider (its
	 * default route is the official API). Rule-owned on the provider; the
	 * compaction transport pairs it with a per-request effective-URL check
	 * because reroutes leave it stale-true. Vertex is selected by its provider
	 * contract instead. Default: false.
	 */
	firstPartyProvider?: boolean;
	/**
	 * Whether the provider honors Claude Code's `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS`
	 * kill switch, which withholds pre-release fields (`context_management`, beta
	 * tool fields, task budgets, structured-output format, `thinking.block_binding`,
	 * server compaction) with their betas. Rule-owned on the
	 * provider whose `ANTHROPIC_*` env routing Claude Code shares. Default: false.
	 */
	honorsClaudeCodeBetaKillSwitch?: boolean;
	/**
	 * Whether requests may carry `output_config.effort` (and its effort beta
	 * header). Vertex AI rejects the field/header. Default: true.
	 */
	supportsOutputEffort?: boolean;
	/**
	 * Stream-watchdog idle-timeout fallback in ms for slow reasoning hosts.
	 * Set to 0 to disable the inter-event idle watchdog entirely, matching
	 * `OpenAICompat.streamIdleTimeoutMs`.
	 *
	 * When unset, direct Anthropic streams use `PI_STREAM_IDLE_TIMEOUT_MS`,
	 * then the legacy `PI_OPENAI_STREAM_IDLE_TIMEOUT_MS` alias, then 300s.
	 */
	streamIdleTimeoutMs?: number;
	/**
	 * Drop the top-level `strict: true` field on tool definitions. Vertex AI's
	 * Anthropic-compatible endpoint rejects unknown tool fields with
	 * `tools.<n>.custom.strict: Extra inputs are not permitted`.
	 */
	disableStrictTools?: boolean;
	/**
	 * The endpoint is Amazon Bedrock's Anthropic Messages API (`/anthropic` on
	 * bedrock-runtime or bedrock-mantle). Requests drop tool `strict`, fit
	 * `metadata.user_id` to Bedrock's request-metadata pattern, and may use
	 * on-demand compaction. Detected from the model `baseUrl`; set it in
	 * models.yml `compat` for a route the URL check cannot see (a proxy, an
	 * `ANTHROPIC_BASE_URL` reroute) or to `false` to opt out.
	 */
	bedrockMessagesApi?: boolean;
	/**
	 * Map adaptive thinking (`thinking: { type: "adaptive" }`) to
	 * `{ type: "enabled", budget_tokens }`. Vertex AI rejects the `adaptive`
	 * tag with `Input tag 'adaptive' ... does not match any of the expected
	 * tags: 'disabled', 'enabled'`.
	 */
	disableAdaptiveThinking?: boolean;
	/** Whether tools may include Anthropic's per-tool eager_input_streaming flag. Default: true for the canonical Anthropic API. */
	supportsEagerToolInputStreaming?: boolean;
	/** Whether long prompt-cache retention (`ttl: "1h"`) is supported. Default: true for canonical Anthropic API. */
	supportsLongCacheRetention?: boolean;
	/** Whether a `ttl: "1h"` breakpoint on API-key requests adds the `extended-cache-ttl-2025-04-11` beta. Unset: true (Vertex rawPredict URLs never send it). */
	extendedCacheTtlBeta?: boolean;
	/**
	 * Whether mid-conversation `role: "system"` messages are accepted in the
	 * `messages` array. When unset, auto-detected from model and deployment policy.
	 */
	supportsMidConversationSystem?: boolean;
	/** Whether turn-scoped system messages accept `clear_at`. */
	supportsTurnScopedSystem?: boolean;
	/** Whether tool availability can change through system-message tool references. */
	supportsMidConversationToolChanges?: boolean;
	/** Whether effort can change through a per-message `output_config`. */
	supportsPerMessageEffort?: boolean;
	/**
	 * Whether the endpoint accepts `thinking.block_binding` and reports
	 * `input_transformations` under the thinking-binding-controls beta.
	 */
	supportsThinkingBindingControls?: boolean;
	/**
	 * Whether the model replaces `thinking: { type: "disabled" }` with
	 * `thinking: { type: "between_tools" }` (Claude Sonnet 5.5). The disabled
	 * form is rejected with a 400; `between_tools` skips up-front thinking and
	 * only allows progress updates between tool calls.
	 */
	supportsBetweenToolsThinking?: boolean;
	/**
	 * Whether the model accepts a forced `tool_choice` (`{ type: "any" }` or
	 * `{ type: "tool", name }`). Claude Fable/Mythos 5 reject forced tool use
	 * outright ("tool_choice forces tool use is not compatible with this model");
	 * the request builder downgrades forced choices to `auto` when this is false.
	 * When unset, auto-detected from the model id. Default: true.
	 */
	supportsForcedToolChoice?: boolean;
	/**
	 * Whether the model accepts sampling parameters (`temperature`, `top_p`,
	 * `top_k`). Opus 4.7+ and Fable/Mythos reject them with a 400. When unset,
	 * auto-detected from the model id. Default: true.
	 */
	supportsSamplingParams?: boolean;
	/**
	 * Include a non-standard `id` field (aliasing `tool_use_id`) on
	 * `tool_result` blocks. Z.AI's Anthropic-compatible proxy deserializes
	 * tool results into a class that reads `.id` (issue #814). Default:
	 * auto-detected (Z.AI hosts).
	 */
	requiresToolResultId?: boolean;
	/**
	 * Allow configured Claude Code fingerprint headers to replace generated
	 * OAuth defaults on non-official Anthropic endpoints.
	 */
	allowAnthropicHeaderOverrides?: boolean;
	/**
	 * Replay unsigned `thinking` blocks from prior assistant turns as native
	 * thinking instead of demoting them to text. Official Anthropic enforces
	 * signature-based thinking-chain integrity, so unsigned blocks must stay
	 * text there; compatible reasoning endpoints (Z.AI, DeepSeek, …) emit
	 * unsigned blocks and expect them back as `type: "thinking"` (#2005).
	 * Default: auto-detected from provider/baseUrl and `model.reasoning`.
	 */
	replayUnsignedThinking?: boolean;
	/**
	 * Whether a thinking-mode history must carry a `thinking` block on EVERY
	 * assistant turn of the current turn. DeepSeek's Anthropic endpoint rejects
	 * the whole request (`The content[].thinking in the thinking mode must be
	 * passed back to the API.`) when any assistant turn after the last user text
	 * lacks one, including turns another upstream in the same gateway model group
	 * produced without reasoning. Presence is all it validates (the block text may
	 * be a placeholder, the signature may be empty), and only `thinking` counts —
	 * `redacted_thinking` does not, and that endpoint rejects the block outright.
	 * A model group that may fail over to such an endpoint must set this: the
	 * stricter member defines the replay contract for the group.
	 * Default: false (official Anthropic and lax proxies tolerate a turn without
	 * thinking and the block is not invented for them).
	 */
	requiresThinkingPresence?: boolean;
	/**
	 * Whether the endpoint requires `thinking.type: "enabled"` whenever the
	 * model reasons. Use for models that reject omitted or disabled thinking.
	 */
	requiresThinkingEnabled?: boolean;
	/**
	 * Prefix Anthropic built-in tool names (`web_search`, `code_execution`, ...)
	 * when they are ordinary client tools. Some Anthropic-compatible gateways
	 * intercept those exact names as server tools and return raw search/result
	 * blocks instead of normal `tool_use` calls.
	 */
	escapeBuiltinToolNames?: boolean;
	/**
	 * The configured endpoint enforces Anthropic's signature protocol on
	 * replayed thinking blocks — either the official API itself or a proxy
	 * that forwards to it (GitHub Copilot, ZenMux, Cloudflare AI Gateway's
	 * `/anthropic` route, Google Vertex's `publishers/anthropic/…`).
	 * Downstream transforms strip stale cross-model thinking signatures on
	 * these endpoints so the signing proxy doesn't 400 with
	 * `Invalid signature in thinking block` (#4297), and adaptive-thinking
	 * models keep the interleaved-thinking beta on them (#6717). Known hosts
	 * are auto-detected from provider id and baseUrl; set this to mark an
	 * opaque signing proxy the URL list can't recognize. Superset of
	 * {@link ResolvedAnthropicCompat.officialEndpoint}.
	 */
	signingEndpoint?: boolean;
	/**
	 * Inject the Claude Code identity instruction on OAuth requests. Haiku 3.5
	 * predates the framing and rejects it; class rules set `false` there.
	 */
	injectClaudeCodeInstruction?: boolean;
	/** Strip image inputs before encoding (text-only serving of a multimodal id). */
	stripImageInput?: boolean;
	/**
	 * Largest width or height a single image block may carry. The canonical
	 * Anthropic API rejects anything larger with `At least one of the image
	 * dimensions exceed max allowed size: 8000 pixels`, and the rejection
	 * poisons every retry and model fallback because the block stays in
	 * context, so the request builder downscales to this value first. Hosts
	 * with a different image contract override it. Default: 8000.
	 */
	maxImageDimension?: number;
	/**
	 * Largest base64 payload a single image block may carry, in bytes. The
	 * canonical Anthropic API measures the encoded string, not the decoded
	 * bytes (`image exceeds 10 MB maximum: 14012300 bytes > 10485760 bytes`),
	 * and an image inside {@link maxImageDimension} can still cross it, so
	 * payload size is clamped on its own. Hosts with a different image
	 * contract override it. Default: 10485760.
	 */
	maxImagePayloadBytes?: number;
	/**
	 * Drop the enabled `thinking` config and replayed thinking blocks when the
	 * conversation is not thinking-led (a proxy contract for non-interleaved
	 * budget models), and replay history without thinking blocks on every turn
	 * that sends `thinking: { type: "disabled" }`. Default: false.
	 */
	stripThinkingHistory?: boolean;
	/**
	 * Send the `effort-2025-11-24` beta exactly when `output_config.effort`
	 * rides the request. Unset keeps the transport's legacy heuristic.
	 */
	effortBeta?: boolean;
	/**
	 * Wire form of a disabled-thinking turn: omit the field, send
	 * `{ type: "disabled" }`, keep adaptive thinking, or send
	 * `{ type: "between_tools" }`. Unset keeps the direct-provider behavior.
	 */
	disabledThinking?: "omit" | "disabled" | "adaptive" | "between-tools";
	/**
	 * `output_config.effort` pinned on a `between_tools` turn. Unset leaves
	 * the turn's effort untouched.
	 */
	betweenToolsEffort?: "low" | "medium" | "high";
	/** The model is a fast-mode SKU: send `speed: "fast"` with the fast-mode beta. Default: false. */
	fastMode?: boolean;
}
interface BedrockCompat {
	/** Explicit disabled-thinking wire form; unset preserves the provider's existing behavior. */
	disabledThinking?: AnthropicCompat["disabledThinking"];
	/** Whether this endpoint accepts no checkpoints, automatic caching, or explicit cachePoint blocks. */
	promptCacheMode?: "none" | "automatic" | "explicit";
	/** Whether this wire may revise already-streamed text (`stream-revision` axis). Unassigned: append-only. */
	streamRevision?: "none" | "possible";
	/** Whether explicit cachePoint blocks accept `ttl: "1h"`; omitted TTL means Bedrock's 5-minute default. */
	supportsLongPromptCacheRetention?: boolean;
	/**
	 * Bedrock-enforced minimum prompt-prefix tokens for an effective checkpoint.
	 * Capability metadata only: emitters must not estimate local token counts.
	 * Zero means no explicit checkpoints.
	 */
	promptCacheMinimumTokens?: number;
	/**
	 * Bedrock-enforced maximum explicit cache checkpoints per request.
	 * Capability metadata only; zero means no explicit checkpoints.
	 */
	promptCacheMaximumCheckpoints?: number;
	/**
	 * Stream-watchdog idle-timeout fallback in ms; 0 disables the idle watchdog.
	 * Undefined defers to `PI_STREAM_IDLE_TIMEOUT_MS`, then the legacy
	 * `PI_OPENAI_STREAM_IDLE_TIMEOUT_MS` alias, then the 300s default.
	 */
	streamIdleTimeoutMs?: number;
	/**
	 * Whether the served model accepts explicit sampling parameters
	 * (`temperature`, `topP`, …). Class rules set `false` for model lines that
	 * reject them on every host. Unassigned: accepted.
	 */
	supportsSamplingParams?: boolean;
	/**
	 * Whether the model accepts a forced `toolChoice` (`any` / `tool`). Claude
	 * Opus/Sonnet 5.5 reject it outright; the request builder downgrades forced
	 * choices to `auto` when this is false. Default: true.
	 */
	supportsForcedToolChoice?: boolean;
	/**
	 * Whether the endpoint accepts thinking-binding controls (`thinking.block_binding`,
	 * the `thinking-binding-controls-2026-08-01` beta and the `/input_transformations`
	 * response path) on prefix-bound models. Only consulted when
	 * `thinking.prefixBinding` is set; `false` omits the controls while keeping the
	 * local prefix-binding safeguards. Default: true.
	 */
	supportsThinkingBindingControls?: boolean;
	/**
	 * Whether the model supports a compaction-only Bedrock Runtime
	 * InvokeModelWithResponseStream request. Ordinary turns still use Converse.
	 */
	supportsServerCompaction?: boolean;
}
interface ResolvedBedrockCompat {
	/** See {@link BedrockCompat.disabledThinking}. */
	disabledThinking?: BedrockCompat["disabledThinking"];
	promptCacheMode: NonNullable<BedrockCompat["promptCacheMode"]>;
	/** See {@link BedrockCompat.streamRevision}. */
	streamRevision?: BedrockCompat["streamRevision"];
	supportsLongPromptCacheRetention: boolean;
	promptCacheMinimumTokens: number;
	promptCacheMaximumCheckpoints: number;
	/** See {@link BedrockCompat.supportsSamplingParams}. */
	supportsSamplingParams?: boolean;
	supportsForcedToolChoice: boolean;
	/** See {@link BedrockCompat.supportsThinkingBindingControls}. */
	supportsThinkingBindingControls: boolean;
	/** See {@link BedrockCompat.supportsServerCompaction}; false unless the Bedrock model-card policy opts in. */
	supportsServerCompaction: boolean;
	/**
	 * Stream-watchdog idle-timeout fallback in ms for hosts with no keepalive
	 * events; 0 disables the idle watchdog. Undefined defers to
	 * `PI_STREAM_IDLE_TIMEOUT_MS`, then the legacy
	 * `PI_OPENAI_STREAM_IDLE_TIMEOUT_MS` alias, then the 300s default.
	 */
	streamIdleTimeoutMs?: number;
}
interface OpenRouterRouting {
	/** List of provider slugs to exclusively use for this request (e.g., ["amazon-bedrock", "anthropic"]). */
	only?: string[];
	/** List of provider slugs to try in order (e.g., ["anthropic", "openai"]). */
	order?: string[];
}
interface VercelGatewayRouting {
	/** List of provider slugs to exclusively use for this request (e.g., ["bedrock", "anthropic"]). */
	only?: string[];
	/** List of provider slugs to try in order (e.g., ["anthropic", "openai"]). */
	order?: string[];
	/** Enables Vercel AI Gateway's provider-aware automatic prompt caching. */
	caching?: "auto";
	/** Stable Responses input-item prefix to anchor for automatic caching. */
	cacheAnchorItems?: number;
	/** Requested automatic-cache lifetime for the Responses API. */
	cacheTtl?: "5m" | "1h";
}
type ResolvedToolStrictMode = NonNullable<OpenAICompat["toolStrictMode"]> | "mixed";
interface ResolvedOpenAISharedCompat {
	supportsDeveloperRole: boolean;
	supportsStrictMode: boolean;
	supportsReasoningEffort: boolean;
	trustExplicitThinkingOnly?: boolean;
	reasoningEffortMap: Partial<Record<Effort, string>>;
	supportsReasoningParams: boolean;
	supportsSamplingParams: boolean;
	supportsPenaltyAndStopParams: boolean;
	thinkingFormat: OpenAIReasoningFormat;
	/** Kimi Code transport selected by live per-model protocol metadata. */
	kimiApiFormat?: OpenAICompat["kimiApiFormat"];
	reasoningDisableMode: OpenAIReasoningDisableMode;
	omitReasoningEffort: boolean;
	includeEncryptedReasoning: boolean;
	filterReasoningHistory: boolean;
	disableReasoningOnForcedToolChoice: boolean;
	disableReasoningOnToolChoice: boolean;
	disableReasoningWithTools?: boolean;
	supportsToolChoice: boolean;
	supportsForcedToolChoice: boolean;
	supportsNamedToolChoice: boolean;
	reasoningContentField?: OpenAICompat["reasoningContentField"];
	mistralReasoningContentParts?: boolean;
	requiresReasoningContentForToolCalls: boolean;
	requiresReasoningContentForAllAssistantTurns: boolean;
	allowsSyntheticReasoningContentForToolCalls: boolean;
	/** See {@link OpenAICompat.syntheticReasoningContentFallback}. */
	syntheticReasoningContentFallback?: string;
	replayReasoningContent: boolean;
	qwenPreserveThinking: boolean;
	qwenTemplateReasoningEffort: boolean;
	requiresThinkingAsText: boolean;
	requiresMistralToolIds: boolean;
	requiresToolResultName: boolean;
	requiresAssistantAfterToolResult: boolean;
	requiresAssistantContentForToolCalls: boolean;
	stripDeepseekSpecialTokens: boolean;
	streamMarkupHealingPattern?: OpenAIStreamMarkupHealingPattern;
	/** See {@link OpenAICompat.streamRevision}. */
	streamRevision?: OpenAICompat["streamRevision"];
	/** See {@link OpenAICompat.streamFirstEventTimeoutMs}. */
	streamFirstEventTimeoutMs?: number;
	reasoningDeltasMayBeCumulative: boolean;
	emptyLengthFinishIsContextError: boolean;
	usesOpenAIToolCallIdLimit: boolean;
	promptCacheSessionHeader?: OpenAICompat["promptCacheSessionHeader"];
	sessionHeader?: OpenAICompat["sessionHeader"];
	/**
	 * Whether this model accepts explicit OpenAI prompt-cache breakpoints.
	 * Built catalog models always materialize this false-by-default value;
	 * optionality preserves hand-authored resolved compat fixtures.
	 */
	supportsPromptCacheBreakpoints?: boolean;
	/** Minimum cache lifetime supported by explicit OpenAI prompt-cache breakpoints. */
	promptCacheBreakpointTtl?: "30m";
	/** The model sits behind OpenRouter (routing prefs and max-token omission apply). */
	isOpenRouterHost: boolean;
	/** Whether this endpoint needs a max-token field even when caller did not set one. */
	alwaysSendMaxTokens: boolean;
	/** Clamp a requested output-token count to the model's advertised ceiling. */
	clampOutputToModelMax: boolean;
	openRouterRouting?: OpenAICompat["openRouterRouting"];
	/** Provider-specific wire model-id transform applied to the base id. */
	wireModelIdMode: "raw" | "cline-pass" | "firepass" | "fireworks" | "openrouter";
	/** See {@link OpenAICompat.toolSchemaFlavor}. Read by both wire paths when converting tools. */
	toolSchemaFlavor?: OpenAICompat["toolSchemaFlavor"];
	/** Strip image inputs before encoding (text-only serving of a multimodal id). */
	stripImageInput: boolean;
	/** Flatten/reject leftover root `anyOf`/`oneOf` unions in strict tool schemas (xAI's function-calling validator 400s on them). */
	rejectRootObjectUnion: boolean;
	/** Retry without strict tools when the host rejects a strict grammar as too large (OpenRouter-Anthropic compiled-grammar overflow). */
	retryWithoutStrictOnGrammarError: boolean;
}
type ResolvedOpenAICompat = ResolvedOpenAISharedCompat & Required<Omit<OpenAICompat, "supportsDeveloperRole" | "supportsReasoningEffort" | "trustExplicitThinkingOnly" | "reasoningEffortMap" | "supportsReasoningParams" | "supportsReasoningSummary" | "statefulResponses" | "supportsSamplingParams" | "supportsPenaltyAndStopParams" | "thinkingFormat" | "kimiApiFormat" | "reasoningDisableMode" | "omitReasoningEffort" | "includeEncryptedReasoning" | "filterReasoningHistory" | "disableReasoningOnForcedToolChoice" | "disableReasoningOnToolChoice" | "disableReasoningWithTools" | "supportsToolChoice" | "supportsForcedToolChoice" | "supportsNamedToolChoice" | "reasoningContentField" | "mistralReasoningContentParts" | "requiresReasoningContentForToolCalls" | "requiresReasoningContentForAllAssistantTurns" | "allowsSyntheticReasoningContentForToolCalls" | "syntheticReasoningContentFallback" | "replayReasoningContent" | "qwenPreserveThinking" | "qwenTemplateReasoningEffort" | "requiresThinkingAsText" | "requiresMistralToolIds" | "requiresToolResultName" | "requiresAssistantAfterToolResult" | "requiresAssistantContentForToolCalls" | "stripDeepseekSpecialTokens" | "stripThinkingHistory" | "streamMarkupHealingPattern" | "reasoningDeltasMayBeCumulative" | "emptyLengthFinishIsContextError" | "usesOpenAIToolCallIdLimit" | "promptCacheSessionHeader" | "sessionHeader" | "supportsPromptCacheBreakpoints" | "promptCacheBreakpointTtl" | "openRouterRouting" | "isOpenRouterHost" | "supportsStrictMode" | "supportsLongPromptCacheRetention" | "alwaysSendMaxTokens" | "clampOutputToModelMax" | "wireModelIdMode" | "vercelGatewayRouting" | "extraBody" | "toolStrictMode" | "toolSchemaFlavor" | "streamFirstEventTimeoutMs" | "streamIdleTimeoutMs" | "streamRevision" | "cacheControlFormat" | "thinkingKeep" | "strictResponsesPairing" | "supportsImageDetailOriginal" | "supportsConfigurationUpdate" | "supportsSteering" | "stripImageInput" | "whenThinking">> & {
	vercelGatewayRouting?: OpenAICompat["vercelGatewayRouting"];
	extraBody?: OpenAICompat["extraBody"];
	cacheControlFormat?: OpenAICompat["cacheControlFormat"];
	thinkingKeep?: OpenAICompat["thinkingKeep"];
	streamIdleTimeoutMs?: number;
	stripThinkingHistory?: boolean;
	/** The configured `baseUrl` is the official OpenAI API (provider `openai`); unset otherwise. */
	officialEndpoint?: boolean;
	toolStrictMode: ResolvedToolStrictMode;
	/** The model sits behind Vercel AI Gateway. */
	isVercelGatewayHost: boolean;
	/** Send the normalized prompt-cache key as top-level `prompt_cache_key` on chat completions. */
	supportsPromptCacheKey: boolean;
	dropThinkingWhenReasoningEffort: boolean;
	/** Complete alternate view for thinking-engaged requests; swap pointers, never spread. */
	whenThinking?: ResolvedOpenAICompat;
};
interface ResolvedOpenAIResponsesCompat extends ResolvedOpenAISharedCompat {
	supportsLongPromptCacheRetention: boolean;
	strictResponsesPairing: boolean;
	supportsImageDetailOriginal: boolean;
	supportsObfuscationOptOut: boolean;
	/**
	 * Whether `reasoning.context: "all_turns"` (full cross-turn reasoning
	 * replay) is accepted. Rule-owned: gpt-5.4+ wire generation on the Codex
	 * transport; earlier ids reject the value.
	 */
	supportsAllTurnsReasoningContext: boolean;
	/**
	 * Whether a `configuration_update` input item may change `reasoning.effort`
	 * mid-conversation while the request-level effort stays byte-stable for
	 * prompt caching. Rule-owned: the GPT-6 family; earlier models do not
	 * support the item type.
	 */
	supportsConfigurationUpdate: boolean;
	/**
	 * Whether the WebSocket transport may send `response.steer` to deliver user
	 * input into the in-flight response. Rule-owned: GPT-6 family.
	 */
	supportsSteering: boolean;
	/** Inject the `# Juice: 0 !important` developer item when reasoning is forced off (gpt-5.6+). */
	requiresReasoningOffJuiceInstruction: boolean;
	/**
	 * Whether `reasoning.summary` may be sent. First-party xAI `/v1/responses`
	 * rejects the field; handlers pass `null` so the wire omits it instead of
	 * filling `"auto"`.
	 */
	supportsReasoningSummary: boolean;
	/** Optional chaining override; unset falls back to officialEndpoint at request time. */
	statefulResponses?: boolean;
	/**
	 * Whether the host can store Responses results server-side (`store: true`).
	 * Rule-owned: hosts that keep generating after a client disconnect so a
	 * dropped stream can resume via `GET /responses/{id}`. Storage is opt-in
	 * per request (`storeResponses` stream option, else
	 * `PI_MUSE_STORE_RESPONSES`); stored runs retain prompts and outputs on the provider.
	 */
	storeResponses: boolean;
	/**
	 * Prefix the `openai-responses` handler puts on every function-tool name it
	 * declares or replays, and strips from returned calls through the request's
	 * wire-name table. Rule-owned: hosts that reject requests declaring certain
	 * exact tool names (Muse Code). Unset sends names unchanged. Other
	 * Responses-family handlers do not read it.
	 */
	toolNamePrefix?: string;
	/**
	 * Whether the host binds native history items (`encrypted_content`, item
	 * ids) to the connection that issued them, so a new process must rebuild
	 * prior turns from message content until its first successful response.
	 * Rule-owned: GitHub Copilot.
	 */
	connectionBoundNativeHistory: boolean;
	streamIdleTimeoutMs?: number;
	vercelGatewayRouting?: OpenAICompat["vercelGatewayRouting"];
	/** The model sits behind Vercel AI Gateway's Responses endpoint. */
	isVercelGatewayHost: boolean;
	/**
	 * The configured endpoint is first-party OpenAI (`provider === "openai"` on
	 * an `api.openai.com` or unset baseUrl), or the Codex backend
	 * (`openai-codex-responses` on the default Codex baseUrl). Gates platform
	 * OpenAI's official-only Responses behavior (default-on stateful
	 * `previous_response_id` chaining and the `text.verbosity` field; the Codex
	 * adapter is not governed by either gate) and host request-size budgets.
	 */
	officialEndpoint: boolean;
	/** Run Harmony-protocol leak detection/mitigation on streamed output. */
	harmonyLeakMitigation: boolean;
	/** Responses-surface prompt-cache marker dialect (OpenRouter-Anthropic `cache_control`). */
	cacheControlFormat?: OpenAICompat["cacheControlFormat"];
}
type ResolvedOpenRouterCompat = ResolvedOpenAICompat & ResolvedOpenAIResponsesCompat;
type ResolvedAnthropicCompat = Required<Omit<AnthropicCompat, "streamIdleTimeoutMs" | "bedrockMessagesApi" | "effortBeta" | "extendedCacheTtlBeta" | "disabledThinking" | "betweenToolsEffort" | "stripThinkingHistory" | "fastMode" | "maxImageDimension" | "maxImagePayloadBytes">> & {
	/** Effort-beta override; undefined keeps the transport's legacy heuristic. */
	effortBeta?: AnthropicCompat["effortBeta"];
	/** Extended-cache-ttl beta opt-out; undefined sends it where a 1h breakpoint needs it. */
	extendedCacheTtlBeta?: AnthropicCompat["extendedCacheTtlBeta"];
	/** Disabled-thinking wire form; undefined keeps the direct-provider behavior. */
	disabledThinking?: AnthropicCompat["disabledThinking"];
	/** Effort pinned on `between_tools` turns; undefined leaves effort untouched. */
	betweenToolsEffort?: AnthropicCompat["betweenToolsEffort"];
	/** Strip thinking history on non-thinking-led and disabled-thinking turns; undefined behaves as false. */
	stripThinkingHistory?: AnthropicCompat["stripThinkingHistory"];
	/** Fast-mode SKU; undefined behaves as false. */
	fastMode?: AnthropicCompat["fastMode"];
	/**
	 * Largest width or height a single image block may carry. Undefined defers
	 * to the canonical Anthropic API limit applied by the request builder.
	 */
	maxImageDimension?: number;
	/**
	 * Largest base64 image payload a single block may carry, in bytes.
	 * Undefined defers to the canonical Anthropic API limit applied by the
	 * request builder.
	 */
	maxImagePayloadBytes?: number;
	/**
	 * Bedrock's Anthropic Messages API (see {@link AnthropicCompat.bedrockMessagesApi}).
	 * `true` when detected or configured; otherwise unset, so rows baked before
	 * this field keep matching.
	 */
	bedrockMessagesApi?: boolean;
	/**
	 * Stream-watchdog idle-timeout fallback in ms for slow reasoning hosts; 0 disables the idle watchdog.
	 * Undefined defers to `PI_STREAM_IDLE_TIMEOUT_MS`, then the legacy
	 * `PI_OPENAI_STREAM_IDLE_TIMEOUT_MS` alias, then 300s.
	 */
	streamIdleTimeoutMs?: number;
	/**
	 * The configured endpoint is the official first-party Anthropic API
	 * (https + exact `api.anthropic.com` host; a missing baseUrl counts as
	 * official because dispatch defaults there). Gates OAuth framing, custom
	 * env headers, and cache-TTL shaping without per-request URL parsing.
	 */
	officialEndpoint: boolean;
	/**
	 * The model is served by the first-party Anthropic provider. Unlike
	 * `officialEndpoint` (URL-derived per deployment), this is provider
	 * identity for compaction gating, always paired with a per-request
	 * effective-URL check.
	 */
	firstPartyProvider: boolean;
};
interface DevinCompat {
	/**
	 * Trust only explicit `thinking` metadata; never derive a thinking surface
	 * from model identity. A reasoning model with no explicit routed thinking
	 * resolves to `thinking: undefined` (`reasoning: true`, no controllable
	 * effort) instead of a fabricated minimal/low/medium/high ladder.
	 */
	trustExplicitThinkingOnly?: boolean;
	/**
	 * Server-side router model. Providers must resolve the router through
	 * `AssignModel` and send the returned assignment JWT with the chat request.
	 */
	modelRouter?: boolean;
	/** Whether the upstream model supports native parallel tool calls. */
	supportsParallelToolCalls?: boolean;
	/** See {@link BedrockCompat.supportsSamplingParams}. */
	supportsSamplingParams?: boolean;
	/**
	 * Smallest explicit `temperature` this wire accepts. Cascade rejects
	 * `temperature: 0` with an opaque `invalid_argument` while any positive
	 * value works, so callers that pin 0 for deterministic decoding (session
	 * titles, memory extraction) would fail and silently fall back to another
	 * model. Unset sends the caller's value through unchanged.
	 */
	temperatureFloor?: number;
}
type ResolvedDevinCompat = Required<Omit<DevinCompat, "supportsSamplingParams" | "temperatureFloor">> & Pick<DevinCompat, "supportsSamplingParams" | "temperatureFloor">;
interface GoogleCompat {
	/** Whether functionCall/functionResponse parts carry the `id` field. */
	supportsFunctionPartId?: boolean;
	/** Add the bypass sentinel to every unsigned Gemini function call. */
	requiresSkipThoughtSignature?: boolean;
	/** Add the bypass sentinel when a Gemini turn's first function call is unsigned. */
	requiresSkipThoughtSignatureOnFirstFunctionCall?: boolean;
	/**
	 * Send the bypass sentinel base64-encoded. For gateways that decode
	 * `thoughtSignature` as bytes and reject the raw literal.
	 */
	base64SkipThoughtSignature?: boolean;
	/** Drop unsigned thinking blocks from replayed history (Antigravity Claude). */
	dropUnsignedThinking?: boolean;
	/** Cloud Code Assist legacy `parameters` schema field instead of `parametersJsonSchema`. */
	ccaLegacyParametersSchema?: boolean;
	/** Whether multimodal (non-text) functionResponse parts are accepted (Gemini 3+). */
	multimodalFunctionResponse?: boolean;
	/** Stream-watchdog first-event timeout in ms; 0 disables it. */
	streamFirstEventTimeoutMs?: number;
	/** Stream-watchdog idle-timeout floor in ms. */
	streamIdleTimeoutMs?: number;
	/** Work around the Flash streaming leak (duplicate first chunk) on gemini-cli. */
	flashStreamLeakWorkaround?: boolean;
	/** Send the Claude thinking beta header on Antigravity Claude requests. */
	claudeThinkingBetaHeader?: boolean;
	/** Antigravity Claude tool-mode request framing. */
	antigravityClaudeToolMode?: boolean;
	/** Usage-label bucket Antigravity reports this model under. */
	antigravityUsageLabel?: string;
	/** Strip image inputs before encoding (text-only serving of a multimodal id). */
	stripImageInput?: boolean;
	/**
	 * Largest width or height a single image may carry for a Claude reader on
	 * this host (see {@link AnthropicCompat.maxImageDimension}). Setting this or
	 * {@link maxImagePayloadBytes} turns on Claude's image clamp, many-image
	 * cap included; unset leaves images as sent.
	 */
	maxImageDimension?: number;
	/**
	 * Largest base64 payload a single image may carry, in bytes (see
	 * {@link AnthropicCompat.maxImagePayloadBytes}).
	 */
	maxImagePayloadBytes?: number;
	/** See {@link BedrockCompat.supportsSamplingParams}. */
	supportsSamplingParams?: boolean;
}
type ResolvedGoogleCompat = Required<Omit<GoogleCompat, "streamFirstEventTimeoutMs" | "streamIdleTimeoutMs" | "antigravityUsageLabel" | "supportsSamplingParams" | "base64SkipThoughtSignature" | "maxImageDimension" | "maxImagePayloadBytes">> & {
	streamFirstEventTimeoutMs?: number;
	streamIdleTimeoutMs?: number;
	antigravityUsageLabel?: string;
	supportsSamplingParams?: boolean;
	base64SkipThoughtSignature?: boolean;
	maxImageDimension?: number;
	maxImagePayloadBytes?: number;
};
interface OllamaCompat {
	/** Omit assistant thinking when the deployment rejects it in history. */
	stripThinkingHistory?: boolean;
}
type ResolvedOllamaCompat = Required<OllamaCompat>;
type CompatConfigOf<TApi extends Api> = TApi extends "openai-completions" | "openrouter" | "openai-responses" | "azure-openai-responses" | "openai-codex-responses" ? OpenAICompat : TApi extends "anthropic-messages" ? AnthropicCompat : TApi extends "bedrock-converse-stream" ? BedrockCompat : TApi extends "devin-agent" ? DevinCompat : TApi extends "google-generative-ai" | "google-vertex" | "google-gemini-cli" ? GoogleCompat : TApi extends "ollama-chat" ? OllamaCompat : undefined;
type CompatOf<TApi extends Api> = TApi extends "openrouter" ? ResolvedOpenRouterCompat : TApi extends "openai-completions" ? ResolvedOpenAICompat : TApi extends "openai-responses" | "azure-openai-responses" | "openai-codex-responses" ? ResolvedOpenAIResponsesCompat : TApi extends "anthropic-messages" ? ResolvedAnthropicCompat : TApi extends "bedrock-converse-stream" ? ResolvedBedrockCompat : TApi extends "devin-agent" ? ResolvedDevinCompat : TApi extends "google-generative-ai" | "google-vertex" | "google-gemini-cli" ? ResolvedGoogleCompat : TApi extends "ollama-chat" ? ResolvedOllamaCompat : undefined;
interface RemoteCompactionConfig<TApi extends Api = Api> {
	/** Enables provider-native compaction for providers not enabled by built-in policy. */
	enabled?: boolean;
	/** Adapter family used by the configured compaction endpoint. */
	api?: TApi;
	/** Absolute V1 compact endpoint URL; when omitted, the adapter derives it from the model base URL. */
	endpoint?: string;
	/** Enables Responses-stream V2 compaction for models verified to support `compaction_trigger`. */
	v2StreamingEnabled?: boolean;
	/** Absolute Responses-stream endpoint URL for V2 compaction; overrides `streamingEndpoint`. */
	v2Endpoint?: string;
	/** Absolute provider streaming endpoint URL used by V2 compaction when no dedicated endpoint is set. */
	streamingEndpoint?: string;
	/** Model id sent to the compaction endpoint when it differs from the active model id. */
	model?: string;
}
interface TokenCost {
	input: number;
	output: number;
	cacheRead: number;
	cacheWrite: number;
}
type ModelPricingStatus = "fixed" | "free" | "included" | "variable" | "unknown";
interface LongContextTokenCost extends TokenCost {
	inputThreshold: number;
	/** Whether the long-context tier starts exactly at `inputThreshold`. */
	inputThresholdInclusive?: boolean;
}
interface PeakPricingWindow {
	weekdays: readonly number[];
	startMinute: number;
	endMinute: number;
}
interface EffectiveTokenCost extends TokenCost {
	effectiveFrom: number;
	longContext?: LongContextTokenCost;
}
interface TimeBasedCost {
	offPeakMultiplier: number;
	peakWindows: readonly PeakPricingWindow[];
	effectiveRates?: readonly EffectiveTokenCost[];
}
type ModelPromptCache = Partial<Record<"short" | "long", number>>;
interface ModelCost extends TokenCost {
	longContext?: LongContextTokenCost;
	timeBased?: TimeBasedCost;
}
type ModelTokenizer = "claude-v3" | "claude-v47" | "claude-v5" | "claude-v5-sonnet" | "qwen3" | "deepseek-v3" | "kimi-k2" | "glm5" | "cl100k" | "o200k";
interface ModelAccountAccess {
	/**
	 * Codex `available_access_programs.cyber`: cyber access programs this account
	 * may request on the model (`standard`, `daybreak_blue`, `daybreak_red`).
	 * Absent when the backend reported no program metadata.
	 */
	cyberPrograms?: readonly string[];
	/**
	 * Antigravity request constants this account's `fetchAvailableModels`
	 * roster advertised, keyed by upstream SKU id. Takes precedence over
	 * {@link Model.antigravityWireProfiles}, which keeps only the first
	 * account's values when multi-account rosters are unioned.
	 */
	antigravityWireProfiles?: Readonly<Record<string, AntigravityModelWireProfile>>;
}
interface CursorModelParameter {
	id: string;
	value: string;
}
interface CursorModelRoute {
	modelId: string;
	parameters: readonly CursorModelParameter[];
	maxMode?: boolean;
}
interface AntigravityModelWireProfile {
	/** Opaque `labels.model_enum` token (`fetchAvailableModels` `model`). */
	modelEnum?: string;
	/** Fixed `generationConfig.maxOutputTokens` the client always sends. */
	maxOutputTokens?: number;
	/** SKU thinking level; the native client sends it in preference to a budget. */
	thinkingLevel?: "LOW" | "MEDIUM" | "HIGH";
	/** SKU thinking budget (`-1` is dynamic), sent when no level is advertised. */
	thinkingBudget?: number;
}
interface Model<TApi extends Api = Api> {
	id: string;
	/** Role-specific runner capability; omitted for ordinary chat models. */
	kind?: ModelKind;
	/**
	 * Verbatim configured kind (models.yml, `modelOverrides`, runtime
	 * registrations). `buildModel` applies it over catalog `kind` rules on every
	 * rebuild, so a configured runner keeps its role.
	 */
	kindConfig?: ModelKind;
	/** Grounding transport supported by this chat model. */
	webSearch?: WebSearchGrounding;
	/** Cheaper same-provider model to run hosted web search in this model's place (model id or provider/id). */
	webSearchModel?: string;
	/** Whether this chat model can carry the Responses `image_generation` tool itself. */
	hostedImage?: boolean;
	/** Same-provider image model to generate images in this model's place (model id or provider/id). */
	imageModel?: string;
	/**
	 * Structured model identity resolved by the compat engine: vendor lineage
	 * class, product family, and revision. Baked into models.json rows and
	 * materialized by `buildModel` for discovered/custom specs.
	 */
	identity: ModelIdentity;
	/**
	 * Whether provider-bound private-use glyphs require reversible ASCII tokenization.
	 * Materialized by `buildModel`; request handlers read this capability instead of
	 * inferring it from the transport API.
	 */
	requiresGlyphTokenization?: boolean;
	/** Whether this model requires Cursor's tool-schema combiner projection. */
	requiresCursorToolSchemaProjection?: boolean;
	/** Whether advertised tool schemas occupy every model request's context (false for deferred catalogs). */
	toolSchemasInContext?: boolean;
	/** Whether this model requires tool-result images hoisted into sibling user content blocks. */
	requiresToolResultImageHoisting?: boolean;
	/**
	 * Whether the host continues a trailing assistant message verbatim instead of
	 * treating it as a completed turn (Ollama, on both its native and OpenAI-compat
	 * endpoints). Callers that need a committed output prefix (session titling)
	 * read this before appending one. Rule-owned via `supports-assistant-prefill`.
	 */
	supportsAssistantPrefill?: boolean;
	/**
	 * Model id to send on the wire when it differs from `id`. Used by catalog
	 * variants that present one upstream model under several local entries —
	 * e.g. GitHub Copilot long-context variants (`claude-opus-4.7-1m` requests
	 * upstream `claude-opus-4.7`; the tier is a client-side context budget, not
	 * a served model id). Providers MUST serialize `requestModelId ?? id`;
	 * everything local (selection, caching, usage attribution) keys on `id`.
	 */
	requestModelId?: string;
	/**
	 * `reasoning.mode` to send on OpenAI Responses-family requests. Set on
	 * generated pro aliases (`gpt-5.6-*-pro` on `openai`/`openai-codex`) that
	 * pair a base wire id (`requestModelId`) with OpenAI's pro reasoning
	 * serving path. Absent everywhere else; providers omit the wire field.
	 */
	reasoningMode?: "pro";
	name: string;
	api: TApi;
	provider: Provider;
	/**
	 * Discovery backend whose catalog policy applies when it differs from the
	 * credential-bearing provider id. Persisted so cached and rebuilt custom
	 * providers retain their transport backend's policy.
	 */
	providerType?: string;
	baseUrl: string;
	reasoning: boolean;
	/**
	 * Exact local tokenizer family resolved from the model identity or supplied
	 * explicitly by a catalog/discovery source. Absent leaves local counting to
	 * the consumer's fallback policy.
	 */
	tokenizer?: ModelTokenizer;
	input: ("text" | "image")[];
	/**
	 * Decoder family used for image inputs when it has narrower format support
	 * than OMP's general image pipeline. `stb` local backends reject WebP.
	 */
	imageInputDecoder?: "stb";
	/**
	 * Most images one request may carry, from user configuration. Replaces the
	 * catalog's `image-count-budget` for hosts no rule names, such as a custom
	 * gateway in front of a vendor with a higher limit.
	 */
	imageCountBudget?: number;
	/**
	 * Native provider tool-call support. `false` is the only unsupported signal:
	 * `true` and `undefined` both mean callers may use native tools. Catalog and
	 * discovery sources should set this sparsely when an upstream explicitly
	 * reports that native tool calling is unsupported.
	 */
	supportsTools?: boolean;
	/** Whether this model accepts the GA OpenAI Responses `{ type: "computer" }` native tool. */
	supportsComputerUse?: boolean;
	/** Verbatim explicit computer-use support from the spec; undefined when `buildModel` inferred the runtime value. */
	supportsComputerUseConfig?: boolean;
	/** GitLab Duo Workflow root namespace selected during catalog discovery. */
	gitlabDuoWorkflowRootNamespaceId?: string;
	/** Cursor `max_mode` request flag returned by `GetUsableModels` for premium models that require max mode. */
	cursorMaxMode?: boolean;
	/**
	 * Per-wire-id `max_mode` markers for the members a collapsed Cursor row
	 * routes to, recorded by `collapseVariants` from live `GetUsableModels`
	 * rows. {@link cursorMaxMode} on a collapsed row is an OR across members,
	 * so it cannot tell a `-low` route that needs no max mode from an Opus
	 * `-fast` route that does; transports look the routed wire id up here
	 * first. Absent on raw rows (their own `cursorMaxMode` already describes
	 * their single wire id) and on bundled snapshots that predate discovery.
	 */
	cursorMaxModeRoutes?: Readonly<Record<string, boolean>>;
	/**
	 * Per-wire-id Antigravity request constants recorded from the account's
	 * `fetchAvailableModels` roster, keyed by upstream SKU id. A collapsed row
	 * carries one entry per member it routes to; a raw row carries its own.
	 * Absent on bundled snapshots that predate discovery.
	 */
	antigravityWireProfiles?: Readonly<Record<string, AntigravityModelWireProfile>>;
	/**
	 * Per-account availability recorded by multi-account discovery: provider
	 * account key (Codex: ChatGPT `chatgpt_account_id`; Antigravity: login
	 * email) → that account's entitlements on this model. An account appears
	 * only when its own catalog lists the model, so credential selection can
	 * route account-gated models (e.g. `gpt-daybreak-blue-latest`, Antigravity
	 * Claude 5.5) straight to eligible accounts. Absent on bundled/config rows.
	 */
	accountAccess?: Readonly<Record<string, ModelAccountAccess>>;
	/** Cursor `RequestedModel.parameters` for this model's default variant. */
	cursorModelParameters?: readonly CursorModelParameter[];
	/**
	 * Per-wire-id Cursor routes recovered from `AvailableModels`. Generic effort
	 * collapse retains these while routing selects one key at request time.
	 */
	cursorModelRoutes?: Readonly<Record<string, CursorModelRoute>>;
	/** Cursor's account-relative model price/multiplier; not a per-token USD rate. */
	cursorPrice?: number;
	/** Cursor requires data retention to invoke this model. */
	cursorRequiresDataRetention?: boolean;
	/** Cursor explicitly reports support for its Agent surface. */
	cursorSupportsAgent?: boolean;
	/** Cursor explicitly reports support for sandboxed execution. */
	cursorSupportsSandboxing?: boolean;
	/** Factory Droid: account-resolved upstream rotation (first entry is the default `x-api-provider`). */
	factoryDroidApiProviders?: string[];
	/** Factory Droid: `configured_order` when live routing chose the rotation; absent means registry order. */
	factoryDroidRoutingSource?: "configured_order";
	/** Factory Droid: server-side refusal fallbacks the account can run; absent when none are named. */
	factoryDroidRefusalFallbacks?: string[];
	/** Factory Droid Standard Credits base rate (relative per-token weight, not dollars). */
	factoryDroidCredits?: number;
	/** Factory Droid dated `credit-rates` cards, ascending; `getFactoryDroidCreditRate` picks the one due now. */
	factoryDroidCreditCards?: readonly DatedCostCard[];
	/** Canonical organization resolved during Factory discovery. */
	factoryDroidOrgId?: string;
	cost: ModelCost;
	/**
	 * Prompt-cache entry lifetime per retention tier, in seconds. Populated only
	 * for providers whose cache-expiry and replay behavior has been validated
	 * for cache warming (direct Anthropic: 5m / 1h). A missing entry means the
	 * provider's cache behavior is unknown — such models are never warmed.
	 * Custom models and provider overrides opt in via models.yml `promptCache`.
	 */
	promptCache?: ModelPromptCache;
	/**
	 * Verbatim configured lifetimes (models.yml, `modelOverrides`, runtime
	 * registrations). `buildModel` applies them over catalog `prompt-cache`
	 * rules on every rebuild; `{}` keeps warming disabled.
	 */
	promptCacheConfig?: ModelPromptCache;
	/**
	 * Interpretation of an all-zero token-rate card. Omitted zero-rate cards
	 * are unknown; any non-zero rate is always treated as fixed pricing.
	 */
	pricingStatus?: Exclude<ModelPricingStatus, "fixed">;
	/** Premium Copilot requests charged per user-initiated request (defaults to 1). */
	premiumMultiplier?: number;
	contextWindow: number | null;
	/** Preserve the host's supplied window instead of applying inferred expansion or reference-price caps. */
	contextWindowAuthoritative?: boolean;
	/** The long-context price tier does not cap the runtime context window when extended context is off. */
	longContextWindowUncapped?: boolean;
	/** Optional larger prompt window available when extended context is enabled. */
	maxContextWindow?: number;
	maxTokens: number | null;
	/**
	 * When `true`, providers MUST omit `max_output_tokens` (Responses) /
	 * `max_tokens` / `max_completion_tokens` (Completions) from the outbound
	 * request and let the upstream API decide the per-response cap. `maxTokens`
	 * is still used locally for budgeting (compaction, context promotion); only
	 * the wire field is suppressed.
	 *
	 * Use this for proxies (notably Ollama) that forward to a backend whose true
	 * output limit OMP cannot discover — sending the wrong value triggers 400s
	 * from the upstream provider.
	 */
	omitMaxOutputTokens?: boolean;
	headers?: Record<string, string>;
	/**
	 * Materialize config-backed headers immediately before a request. Catalog
	 * inspection never invokes this hook; transports receive a cloned model
	 * whose `headers` is a plain resolved record and whose hook is removed.
	 */
	resolveHeaders?: (signal?: AbortSignal) => Promise<Record<string, string> | undefined>;
	/**
	 * Streaming transport override. When `"pi-native"`, `streamSimple` routes
	 * the request to the model's `baseUrl` via the auth-gateway's
	 * `POST /v1/pi/stream` endpoint instead of dispatching the per-API
	 * provider client. The `baseUrl` must point at an `omp auth-gateway`
	 * (or compatible) host; `headers.Authorization` (or `apiKey` resolved by
	 * the registry) carries the gateway bearer.
	 *
	 * Used by containerized omp installs (e.g. robomp slots) to route every
	 * LLM call through a sidecar gateway that holds the real provider
	 * credentials. The model's other metadata (pricing, context window,
	 * thinking config, …) still resolves locally; only the streaming
	 * dispatch is redirected.
	 */
	transport?: "pi-native";
	/** Standard Responses is opt-in and catalog-gated; either model/request false vetoes true. Codex keeps its independent policy. */
	preferWebsockets?: boolean;
	/** Codex Responses Lite transport: send the lite marker and carry instructions/tools as input items (mirrors codex-rs `use_responses_lite`). */
	useResponsesLite?: boolean;
	/**
	 * Service-tier ids the provider advertises for this model (Codex discovery
	 * `service_tiers[].id`, or an OpenAI-compatible `/v1/models` row's
	 * `service_tiers`, e.g. `priority`, `ultrafast`). Absent when the provider
	 * publishes no per-model tier list.
	 */
	serviceTiers?: readonly string[];
	/** Preferred model to switch to when context promotion is triggered (model id or provider/id). */
	contextPromotionTarget?: string;
	/** Preferred model to use only for compaction (model id or provider/id); the active session model is unchanged. */
	compactionModel?: string;
	/** Provider-native compaction endpoint configuration. */
	remoteCompaction?: RemoteCompactionConfig<TApi>;
	/** Provider-assigned priority value (lower = higher priority). */
	priority?: number;
	/**
	 * Per-service-tier cost multipliers baked from the `service-tier-cost`
	 * catalog axis (e.g. `{ priority: 2.5 }`). Absent tiers use the API-generic
	 * defaults, and a tier with no published price stays at 1x.
	 */
	serviceTierCost?: Readonly<Partial<Record<"flex" | "priority" | "ultrafast", number>>>;
	/**
	 * Provider-supplied one-line blurb for this model. Set only when an upstream
	 * ships one (Devin's `GetCliModelConfigs`); never synthesized locally.
	 */
	description?: string;
	/** Upstream marks this model as newly released. */
	isNew?: boolean;
	/** Upstream marks this model as beta / preview quality. */
	isBeta?: boolean;
	/** Authenticated catalog marks this as the account's default; the startup pick prefers it over `default-model`. */
	isProviderDefault?: boolean;
	/** Upstream marks this model as one of its recommended picks. */
	isRecommended?: boolean;
	/** Canonical thinking capability metadata for this model. */
	thinking?: ThinkingConfig;
	/** Intelligence score delivered by the model catalog. */
	int?: number | null;
	/** Catalog-estimated output speed in tokens per second. */
	tps?: number | null;
	/**
	 * Fully-resolved compatibility record, materialized once by `buildModel`.
	 * Protocol handlers read fields; they never detect, resolve, or allocate.
	 */
	compat: CompatOf<TApi>;
	/** Verbatim sparse compat from the spec (user/config intent), for introspection only. */
	compatConfig?: CompatConfigOf<TApi>;
	/**
	 * Which shape to use when exposing the Codex `apply_patch` tool to this model.
	 * Generated catalog policy sets `"freeform"` for first-party GPT-5 Responses
	 * models that support OpenAI custom tools with a Lark grammar. The freeform
	 * variant sends a raw patch string with no JSON envelope.
	 * - `"function"` or undefined: JSON function-tool with `{input: string}` (spec §1.2).
	 */
	applyPatchToolType?: "freeform" | "function";
	/**
	 * Edit-tool description density for this model. `"compact"` selects the
	 * terse mode prompt (all operations and invariants preserved) for hosts
	 * where per-request prompt bytes are the dominant cost. Generated catalog
	 * policy sets it; the edit tool falls back to the full prompt when unset.
	 */
	editPromptVariant?: "full" | "compact";
	/**
	 * Force OAuth-style request shaping for providers whose API key prefix doesn't
	 * match an OAuth token (e.g. routing Anthropic traffic through a proxy that
	 * expects Claude Code framing). When true, the streaming layer sets
	 * `options.isOAuth = true` for the underlying provider call.
	 */
	isOAuth?: boolean;
	/**
	 * Amazon Bedrock Guardrail id or ARN attached to every Converse request for
	 * this model. Set from `providers.amazon-bedrock.guardrailIdentifier`; the
	 * streaming layer forwards it as `options.guardrailIdentifier` so accounts
	 * that gate `bedrock:InvokeModel*` on the `bedrock:GuardrailIdentifier`
	 * condition key stop returning an explicit deny.
	 */
	guardrailIdentifier?: string;
	/** Bedrock guardrail version. Defaults to `"DRAFT"` at request time when unset. */
	guardrailVersion?: string;
	/** Bedrock guardrail trace verbosity. */
	guardrailTrace?: "enabled" | "disabled" | "enabled_full";
	/**
	 * Bedrock invocation-log tags attached to every Converse request for this
	 * model. Set from `providers.<provider>.requestMetadata`; the Bedrock
	 * transport reads it directly and validates it against AWS's limits.
	 */
	requestMetadata?: Record<string, string>;
}
type CacheControlEphemeral = {
	type: "ephemeral";
	ttl?: "1h" | "5m";
	/** Claude Code prompt-caching-scope beta: shares the breakpoint across sessions. */
	scope?: "global";
};
type Base64ImageSource = {
	type: "base64";
	media_type: "image/jpeg" | "image/png" | "image/gif" | "image/webp";
	data: string;
};
type URLImageSource = {
	type: "url";
	url: string;
};
type FileImageSource = {
	type: "file";
	file_id: string;
};
type ImageSource = Base64ImageSource | URLImageSource | FileImageSource;
type TextBlockParam = {
	type: "text";
	text: string;
	cache_control?: CacheControlEphemeral | null;
};
type ImageBlockParam = {
	type: "image";
	source: ImageSource;
	cache_control?: CacheControlEphemeral | null;
};
type ToolUseBlockParam = {
	type: "tool_use";
	id: string;
	name: string;
	input: unknown;
	cache_control?: CacheControlEphemeral | null;
};
type ToolResultBlockParam = {
	type: "tool_result";
	tool_use_id: string;
	content?: string | Array<TextBlockParam | ImageBlockParam>;
	is_error?: boolean;
	cache_control?: CacheControlEphemeral | null;
};
type ServerToolUseBlockParam = {
	type: "server_tool_use";
	id: string;
	name: string;
	input?: Record<string, unknown> | null;
	[key: string]: unknown;
};
type WebSearchToolResultBlockParam = {
	type: "web_search_tool_result";
	tool_use_id: string;
	content: unknown;
	[key: string]: unknown;
};
type ToolSearchToolResultBlockParam = {
	type: "tool_search_tool_result";
	tool_use_id: string;
	content: unknown;
	[key: string]: unknown;
};
type ToolChangeReferenceParam = {
	type: "tool_reference";
	name: string;
};
type ToolAdditionBlockParam = {
	type: "tool_addition";
	tool: ToolChangeReferenceParam;
};
type ToolRemovalBlockParam = {
	type: "tool_removal";
	tool: ToolChangeReferenceParam;
};
type ThinkingBlockParam = {
	type: "thinking";
	thinking: string;
	signature: string;
};
type RedactedThinkingBlockParam = {
	type: "redacted_thinking";
	data: string;
};
type FallbackBlockParam = {
	type: "fallback";
	from: {
		model: string;
	};
	to: {
		model: string;
	};
};
type CompactionBlockParam = {
	type: "compaction";
	content: string;
	/** Opaque signed tool changes from a compaction response. */
	tool_changes?: Record<string, unknown>[];
	signature?: string;
	encrypted_content?: string | null;
	cache_control?: CacheControlEphemeral | null;
};
type ContentBlockParam = TextBlockParam | ImageBlockParam | ToolUseBlockParam | ToolResultBlockParam | ServerToolUseBlockParam | WebSearchToolResultBlockParam | ToolSearchToolResultBlockParam | ToolAdditionBlockParam | ToolRemovalBlockParam | ThinkingBlockParam | RedactedThinkingBlockParam | FallbackBlockParam | CompactionBlockParam;
type MessageParam = {
	role: "user" | "assistant" | "system";
	content: string | ContentBlockParam[];
	/** Turn-scoped system-message lifetime. */
	clear_at?: "never" | "next_user_message";
	/** Per-message effort override. */
	output_config?: OutputConfig;
};
type ToolInputSchema = {
	type: "object";
	properties?: unknown | null;
	required?: string[] | null;
	[k: string]: unknown;
};
type Tool = {
	name: string;
	description?: string;
	input_schema: ToolInputSchema;
	cache_control?: CacheControlEphemeral | null;
	/** Structured-outputs beta: enforce the schema as a strict grammar. */
	strict?: boolean;
	/** Fine-grained tool streaming beta: stream tool input as it is generated. */
	eager_input_streaming?: boolean;
	/** Withhold this tool until a later `tool_addition` block references it. */
	defer_loading?: boolean;
};
type ToolChoiceAuto = {
	type: "auto";
	disable_parallel_tool_use?: boolean;
};
type ToolChoiceAny = {
	type: "any";
	disable_parallel_tool_use?: boolean;
};
type ToolChoiceTool = {
	type: "tool";
	name: string;
	disable_parallel_tool_use?: boolean;
};
type ToolChoiceNone = {
	type: "none";
};
type ToolChoice = ToolChoiceAuto | ToolChoiceAny | ToolChoiceTool | ToolChoiceNone;
type Metadata = {
	user_id?: string | null;
};
type ThinkingBlockBinding = {
	prefix_mismatch_behavior: "drop_block" | "error";
};
type ThinkingConfigEnabled = {
	type: "enabled";
	budget_tokens: number;
	/** Opus 4.7+ reasoning display mode. */
	display?: "summarized" | "omitted";
	/** Preserved-thinking prefix mismatch policy. */
	block_binding?: ThinkingBlockBinding;
};
type ThinkingConfigDisabled = {
	type: "disabled";
};
type ThinkingConfigAdaptive = {
	type: "adaptive";
	/** Opus 4.7+ reasoning display mode. */
	display?: "summarized" | "omitted";
	/** Preserved-thinking prefix mismatch policy. */
	block_binding?: ThinkingBlockBinding;
};
type ThinkingConfigBetweenTools = {
	type: "between_tools";
};
type ThinkingConfigParam = ThinkingConfigEnabled | ThinkingConfigDisabled | ThinkingConfigAdaptive | ThinkingConfigBetweenTools;
type OutputConfig = {
	/** Adaptive-thinking effort level (effort beta). */
	effort?: "low" | "medium" | "high" | "xhigh" | "max" | null;
	/** Structured format, excluded on compaction requests. */
	format?: unknown;
	/** Task-budgets beta. */
	task_budget?: TokenTaskBudget | null;
};
type FallbackParam = {
	model: string;
	max_tokens?: number;
	thinking?: ThinkingConfigParam;
	output_config?: OutputConfig;
	speed?: "fast";
};
type CompactionEdit = {
	type: "compact_20260112";
	/** `input_tokens` is the only trigger; `value` must be at least 50,000. */
	trigger?: {
		type: "input_tokens";
		value: number;
	};
	pause_after_compaction?: boolean;
	/** Replaces the API's default summarization prompt entirely. */
	instructions?: string;
};
type ContextManagement = {
	edits: Array<{
		type: "clear_thinking_20251015";
		keep: "all";
	} | CompactionEdit>;
};
type OnDemandCompaction = {
	type: "summarize";
	instructions?: string;
};
type ThreadParam = {
	type: "create";
} | {
	type: "continue";
	previous_message_id: string;
};
type MessageCreateParams = {
	model: string;
	messages: MessageParam[];
	max_tokens: number;
	system?: string | TextBlockParam[];
	temperature?: number;
	top_p?: number;
	top_k?: number;
	stop_sequences?: string[];
	stream?: boolean;
	tools?: Tool[];
	tool_choice?: ToolChoice;
	metadata?: Metadata;
	thinking?: ThinkingConfigParam;
	output_config?: OutputConfig;
	/** Fast-mode beta: realization of priority service tier. */
	speed?: "fast";
	/** Claude Code context-management beta. */
	context_management?: ContextManagement;
	/** On-demand compaction request, mutually exclusive with context_management. */
	compaction?: OnDemandCompaction;
	/** Google Cloud rawPredict carries Anthropic beta names in the body. */
	anthropic_beta?: string[];
	/**
	 * Server-side fallback beta chain — up to three fallback models the API
	 * retries when a classifier blocks the primary. Required companion beta
	 * header: `server-side-fallback-2026-06-01`.
	 */
	fallbacks?: FallbackParam[];
	/** Fallback credit token redeemed from a prior refusal (`fallback-credit-2026-06-01` / `fallback-credit-2026-07-01`). */
	fallback_credit_token?: string;
	/** Message threads beta: start or continue a server-held conversation. */
	thread?: ThreadParam;
};
type MessageCreateParamsStreaming = MessageCreateParams & {
	stream: true;
};
type StopDetails = {
	type: string;
	category?: string | null;
	explanation?: string | null;
	fallback_credit_token?: string | null;
	fallback_has_prefill_claim?: boolean | null;
};
declare const kStreamingPartialJson: unique symbol;
interface TokenTaskBudget {
	type: "tokens";
	total: number;
	remaining?: number;
}
type MessageAttribution = "user" | "agent";
type ServiceTier = "auto" | "default" | "flex" | "scale" | "priority" | "ultrafast";
type ServiceTierFamily = "openai" | "anthropic" | "google";
type ServiceTierByFamily = Partial<Record<ServiceTierFamily, ServiceTier>>;
interface TextContent {
	type: "text";
	text: string;
	textSignature?: string;
}
interface ThinkingContent {
	type: "thinking";
	thinking: string;
	thinkingSignature?: string;
	itemId?: string;
	/**
	 * `true`: `thinking` is a provider-written summary of the model's reasoning
	 * (OpenAI Responses `summary_text`, Gemini thought summaries). `false`: the
	 * wire delivered it as the reasoning itself, not marked as a summary
	 * (chat-completions reasoning fields, Responses `reasoning_text`, Devin
	 * `thinking`); whether that is the full trace is vouched for per family by
	 * the catalog's `portable-reasoning`. Unset when provenance is unknown,
	 * including turns recorded before parsers set it. Only `false` blocks
	 * replay natively on another host.
	 */
	summary?: boolean;
	/**
	 * `true`: the block is narration — prose the model addressed to the user
	 * between tool calls, which the provider returned as a signed thinking block
	 * (Anthropic connector-text summarization). It stays a thinking block for
	 * replay; user-facing surfaces present it as answer text.
	 */
	narration?: boolean;
}
interface RedactedThinkingContent {
	type: "redactedThinking";
	data: string;
}
interface AnthropicFallbackContent {
	type: "fallback";
	from: {
		model: string;
	};
	to: {
		model: string;
	};
}
interface AnthropicServerToolContent {
	type: "anthropicServerTool";
	block: {
		type: "server_tool_use";
		id: string;
		name: "web_search" | "tool_search_tool_regex" | "tool_search_tool_bm25";
		input?: Record<string, unknown> | null;
		[key: string]: unknown;
	} | {
		type: "web_search_tool_result" | "tool_search_tool_result";
		tool_use_id: string;
		content: unknown;
		[key: string]: unknown;
	};
}
interface ProviderFileReference {
	provider: "openai" | "anthropic" | "google";
	id?: string;
	uri?: string;
	expiresAt?: number;
}
interface ImageContent {
	type: "image";
	data: string;
	mimeType: string;
	/**
	 * OpenAI-only resolution hint. `"original"` preserves native resolution
	 * (required for snapcompact frames, whose glyphs do not survive the
	 * default `auto` downscale). Providers without a detail knob ignore it.
	 */
	detail?: "auto" | "low" | "high" | "original";
	/** Provider-native file reference preferred only by its matching provider. */
	providerFile?: ProviderFileReference;
	/**
	 * Optional https mirror of `data`, served by a caller-run blob server.
	 * Providers whose API fetches remote images send this URL instead of the
	 * base64 payload; every other provider ignores it. `data` remains the
	 * source of truth — the URL must serve exactly those bytes, and callers
	 * are responsible for keeping it stable across turns (prefix caches hash
	 * the URL string, and Anthropic silently forgets images when a resent
	 * turn differs byte-wise).
	 */
	url?: string;
}
type ComputerAction = {
	type: "click";
	button: "left" | "right" | "wheel" | "back" | "forward";
	x: number;
	y: number;
	keys?: string[] | null;
} | {
	type: "double_click";
	x: number;
	y: number;
	keys: string[] | null;
} | {
	type: "drag";
	path: Array<{
		x: number;
		y: number;
	}>;
	keys?: string[] | null;
} | {
	type: "keypress";
	keys: string[];
} | {
	type: "move";
	x: number;
	y: number;
	keys?: string[] | null;
} | {
	type: "screenshot";
} | {
	type: "scroll";
	x: number;
	y: number;
	scroll_x: number;
	scroll_y: number;
	keys?: string[] | null;
} | {
	type: "type";
	text: string;
} | {
	type: "wait";
};
interface ComputerSafetyCheck {
	id: string;
	code?: string | null;
	message?: string | null;
}
interface ComputerToolCallMetadata {
	type: "computer";
	providerItemId: string;
	actions: ComputerAction[];
	pendingSafetyChecks: ComputerSafetyCheck[];
}
type ToolCallProviderMetadata = ComputerToolCallMetadata;
type ComputerScreenshotRef = {
	type: "computer_screenshot";
	image_url: string;
	file_id?: never;
} | {
	type: "computer_screenshot";
	file_id: string;
	image_url?: never;
};
interface ComputerToolResultMetadata {
	type: "computer";
	screenshot: ComputerScreenshotRef;
	acknowledgedSafetyChecks: ComputerSafetyCheck[];
}
type ToolResultProviderMetadata = ComputerToolResultMetadata;
interface ToolCall {
	type: "toolCall";
	id: string;
	name: string;
	arguments: Record<string, unknown>;
	[kStreamingPartialJson]?: string;
	thoughtSignature?: string;
	intent?: string;
	/**
	 * Verbatim in-band syntax block that produced this synthetic `ptc_*` call.
	 * Present only for owned prompt/tool-call formats; provider-native calls omit it.
	 */
	rawBlock?: string;
	/**
	 * Original wire-level name when the tool was invoked via OpenAI's custom-tool
	 * mechanism (e.g., `apply_patch`). Set by `openai-responses` on receive so
	 * the history-replay path can re-emit the call as `custom_tool_call` with
	 * its paired tool-result as `custom_tool_call_output`. Absent for regular
	 * JSON function tools.
	 */
	customWireName?: string;
	/** Responses API namespace for namespaced function and custom tools. */
	namespace?: string;
	/** Provider-native metadata required to execute and faithfully replay this call. */
	providerMetadata?: ToolCallProviderMetadata;
}
type StopReason = "stop" | "length" | "toolUse" | "error" | "aborted";
interface OpenAIResponsesHistoryPayload {
	type: "openaiResponsesHistory";
	provider?: string;
	dt?: boolean;
	items: Array<Record<string, unknown>>;
}
type AnthropicOutputEffort = "low" | "medium" | "high" | "xhigh" | "max";
interface AnthropicToolChange {
	type: "tool_addition" | "tool_removal";
	name: string;
}
interface AnthropicMessagePayload {
	type: "anthropicMessage";
	clearAt?: "never" | "next_user_message";
	effort?: AnthropicOutputEffort;
	toolChanges?: AnthropicToolChange[];
}
interface AnthropicRequestControls {
	/**
	 * `context.messages.length` of the request that produced this response, i.e. the
	 * response's own index. A record found at another index belongs to a history that was
	 * rewritten before it (compaction, dropped messages) and is not replayed as controls.
	 */
	messageIndex: number;
	/** Present when the request kept a stable tool declaration (`supportsMidConversationToolChanges`). Source tool names, not wire names. */
	tools?: {
		/** Top-level `tools` in wire order. */
		declared: string[];
		/** Subset of `declared` sent with `defer_loading: true`. */
		deferred: string[];
		/** Tools active at the end of the request, in `context.tools` order. */
		active: string[];
	};
	/** Present when the request kept a stable effort (`supportsPerMessageEffort`); `null` = API default. */
	effort?: {
		topLevel: AnthropicOutputEffort | null;
		tail: AnthropicOutputEffort | null;
	};
}
interface AnthropicCompactionPayload {
	type: "anthropicCompaction";
	/** Provider that produced the summary; only that provider replays it natively. */
	provider: string;
	content: string;
	/** Signature of an on-demand block; replayed verbatim. */
	signature?: string;
	/** Net tool changes across the summarized range; covered by the signature, replayed verbatim. */
	toolChanges?: Record<string, unknown>[];
	/** Legacy threshold block state (`compact-2026-01-12`); replay-only. */
	encryptedContent?: string;
	/**
	 * Harness-appended file metadata (`<files>` section) kept out of the
	 * byte-identical block. The converter replaces the summary message with the
	 * block and skips its text, so it replays this as a user message after the
	 * block's retained tail: before the first message created after the summary.
	 */
	filesText?: string;
	/**
	 * File metadata of earlier summaries whose replay position lies inside this
	 * summary's retained tail. Retained messages must reach the API unchanged,
	 * so each keeps replaying where it did: before the first message created
	 * after `after` (the earlier summary's commit time).
	 */
	retainedFiles?: AnthropicCompactionFiles[];
	/**
	 * Set on summaries whose retained tail replays unchanged: file metadata
	 * after the tail, earlier metadata at `retainedFiles`. Summaries persisted
	 * without it keep their original layout (metadata after the first retained
	 * turn), since later thinking was signed against those bytes.
	 */
	exactTail?: true;
}
interface AnthropicCompactionFiles {
	text: string;
	/** Replays before the first message created after this time (ms). */
	after: number;
}
interface CursorHistoryPayload {
	type: "cursorHistory";
	/** Wire route the records were written under, as sent (requested model, details id, max mode, parameters). */
	wireRoute: string;
	/** Digest of the turn's opening user message, content and paired tool results when recorded; a rewritten turn rebuilds instead. */
	digest: string;
	/** Record bytes (UTF-8 JSON) in conversation order; each blob id is the SHA-256 of its bytes. */
	records: string[];
}
interface GeminiNativeContentPayload {
	type: "geminiNativeContent";
	/** Digest of the turn's text, thinking and tool calls when recorded; a rewritten turn rebuilds instead. */
	digest: string;
	content: {
		role?: string;
		parts: Record<string, unknown>[];
		[key: string]: unknown;
	};
}
type ProviderPayload = OpenAIResponsesHistoryPayload | AnthropicMessagePayload | AnthropicCompactionPayload | CursorHistoryPayload | GeminiNativeContentPayload;
interface ProviderInputTransformation {
	type: string;
	path?: string;
	reason?: string;
	[key: string]: unknown;
}
interface UserMessage {
	role: "user";
	content: string | (TextContent | ImageContent)[];
	/** Original sender and body of a parent-agent IRC steer, for transcript presentation only. */
	parentIrc?: {
		from: string;
		body: string;
	};
	/** True if the message was injected by the system (e.g., auto-continue). */
	synthetic?: boolean;
	/** True when injected mid-turn as a steer; consumed by the agent's pre-LLM transform to wrap it for emphasis. Never rendered. */
	steering?: boolean;
	/** True when the provider delivered this steer into the response it was streaming (`response.steer`). Display-only; never sent. */
	liveSteered?: boolean;
	/** Timestamp of a client-side history rewrite represented by this message. */
	historyRewriteAt?: number;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	/** Provider-specific opaque payload used to reconstruct transport-native history. */
	providerPayload?: ProviderPayload;
	timestamp: number;
}
interface DeveloperMessage {
	role: "developer";
	content: string | (TextContent | ImageContent)[];
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	/** True if the message was injected by the system (e.g., auto-continue) and initiates a fresh run rather than continuing the current one. */
	synthetic?: boolean;
	/** True when the synthetic prompt was a deliberate operator action (`.`, `c` continue shortcut) rather than an automatic continuation — its timestamp is the turn's prompt time. */
	userInitiated?: boolean;
	/** Provider-specific opaque payload used to reconstruct transport-native history. */
	providerPayload?: ProviderPayload;
	timestamp: number;
}
type AssistantRetryRecoveryKind = "credential" | "model" | "wait" | "plain";
type AssistantRetryRecovery = {
	kind: "auto-retry";
	status: "recovered";
	attempt: number;
	recoveredAt: string;
	recovery: AssistantRetryRecoveryKind;
	note: string;
	supersededBy?: {
		timestamp: number;
		responseId?: string;
		provider: string;
		model: string;
	};
} | {
	kind: "auto-retry";
	status: "superseded";
	attempt: number;
	recovery: AssistantRetryRecoveryKind;
	note: string;
};
interface ContextSnapshot {
	promptTokens: number;
	nonMessageTokens: number;
	/**
	 * Estimated tool-schema tokens included in `nonMessageTokens` (0 when the
	 * model defers schemas server-side). Lets a later model whose schema policy
	 * differs rebase the provider anchor instead of inheriting its schemas.
	 */
	toolSchemaTokens?: number;
	/** Estimated prompt tokens removed by local history rewrites after this provider snapshot was recorded. */
	historyRewriteTokensRemoved?: number;
	/**
	 * Compaction epoch current when this snapshot's provider request was recorded.
	 * A later compaction bumps the session epoch, so an anchor whose epoch is
	 * older than the current in-flight snapshot describes pre-compaction history
	 * and must not override the rebased estimate.
	 */
	compactionEpoch?: number;
	lastMessageTimestamp?: number;
}
interface AssistantMessage {
	role: "assistant";
	content: (TextContent | ThinkingContent | RedactedThinkingContent | AnthropicFallbackContent | AnthropicServerToolContent | ImageContent | ToolCall)[];
	api: Api;
	provider: Provider;
	model: string;
	/** Stored credential row that produced this turn; absent for external or unknown keys. */
	credentialId?: number;
	contextSnapshot?: ContextSnapshot;
	retryRecovery?: AssistantRetryRecovery;
	responseId?: string;
	/**
	 * Name of the upstream provider an aggregator routed this request to, as
	 * reported in the response (e.g. OpenRouter's top-level `provider` field:
	 * `"OpenAI"`, `"Anthropic"`, `"Together"`). Distinct from `provider`, which
	 * is the configured gateway we called (`"openrouter"`). Undefined for direct
	 * providers that expose no such field.
	 */
	upstreamProvider?: string;
	/**
	 * Concrete model that produced this turn when it is knowable independently
	 * of the requested id: reported by a router that selected one, or recovered
	 * from a signed thinking block (Anthropic signatures name the serving
	 * model). Compared against `model` to notice a gateway serving something
	 * other than what was requested.
	 */
	upstreamModel?: string;
	/**
	 * Service tier the provider reported serving this turn, when the API echoes
	 * one (`response.service_tier`), falling back to the tier the request carried
	 * when the response omits the echo. Absent when the provider reports no tier
	 * or the echo cannot be trusted (proxies). This is the tier the turn actually
	 * ran on, which is what cost, premium-request, and speed accounting key on —
	 * the session's live setting may already have changed.
	 */
	serviceTier?: ServiceTier;
	usage: Usage;
	/**
	 * Visible output tokens a history rewrite (shake) has since removed from
	 * `content`. `usage.output` still counts them, so a replay estimate subtracts
	 * them to keep the turn's reasoning without recharging the removed text.
	 */
	elidedOutputTokens?: number;
	stopReason: StopReason;
	stopDetails?: StopDetails | null;
	errorMessage?: string;
	/** Stable recovery-classification text when errorMessage includes display-only diagnostics. */
	errorClassificationMessage?: string;
	/** True only when an exact request-body-read timeout failed on a full Responses replay, not a previous-response delta. */
	requestBodyReadTimeoutFullReplay?: boolean;
	/** Per-tool abort messages used when an aborted assistant turn needs different placeholder results per tool call. */
	toolCallAbortMessages?: Record<string, string>;
	/** HTTP status surfaced by the provider when the request failed. Populated by every provider's catch block alongside `errorMessage` so consumers (auth retry, telemetry, UI) can branch without regex-scraping the message. */
	errorStatus?: number;
	/** Structured machine-readable error classifier; see `utils/error-id.ts` for bit layout and helpers. */
	errorId?: number;
	/**
	 * Stable identifiers for request features the provider silently dropped
	 * during this turn (e.g. `"priority"`). Set when a server-side rejection
	 * triggered an in-provider fallback retry that succeeded without the
	 * feature. Callers can use this to sync user-facing toggles back to the
	 * server's actual state.
	 */
	disabledFeatures?: string[];
	/** Provider-reported input rewrites such as dropped bound-thinking blocks. */
	inputTransformations?: ProviderInputTransformation[];
	/**
	 * Controls an Anthropic request declared, recorded on its response so later
	 * requests over the same transcript replay a byte-identical prefix.
	 */
	requestControls?: AnthropicRequestControls;
	/** Provider-specific opaque payload used to reconstruct transport-native history. */
	providerPayload?: ProviderPayload;
	/** In-memory fallback credit handle attached when a refusal response carries a fallback credit token. */
	fallbackCreditHandle?: AnthropicFallbackCreditHandle;
	timestamp: number;
	duration?: number;
	ttft?: number;
	/** Local wall-clock time the response finished streaming (ms since epoch); stamped by the session at message_end so prompt→yield timing never depends on provider-reported duration. */
	completedAt?: number;
}
interface ToolResultMessage<TDetails = unknown> {
	role: "toolResult";
	toolCallId: string;
	toolName: string;
	content: (TextContent | ImageContent)[];
	details?: TDetails;
	isError: boolean;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	/** Timestamp when output was pruned (ms since epoch). Undefined if unpruned. */
	prunedAt?: number;
	/** Provider-native metadata required to faithfully replay this result. */
	providerMetadata?: ToolResultProviderMetadata;
	/**
	 * Tool-declared: this result carried no information worth retaining once
	 * consumed (zero matches, elapsed wait). Compaction passes may elide it.
	 * Never set together with isError.
	 */
	useless?: boolean;
	timestamp: number;
}
type Message = UserMessage | DeveloperMessage | AssistantMessage | ToolResultMessage;
interface ToolCallExample<TArgs = Record<string, unknown>> {
	caption?: string;
	call: TArgs;
}
interface ToolCompareExample<TArgs = Record<string, unknown>> {
	caption?: string;
	bad: TArgs;
	good: TArgs;
}
interface ToolNoteExample {
	caption: string;
	note?: string;
}
type ToolExample<TArgs = Record<string, unknown>> = ToolCallExample<TArgs> | ToolCompareExample<TArgs> | ToolNoteExample;
type AssistantMessageEvent = {
	type: "start";
	contentIndex?: undefined;
	partial: AssistantMessage;
} | {
	type: "text_start";
	contentIndex: number;
	partial: AssistantMessage;
} | {
	type: "text_delta";
	contentIndex: number;
	delta: string;
	partial: AssistantMessage;
} | {
	type: "text_end";
	contentIndex: number;
	content: string;
	partial: AssistantMessage;
} | {
	type: "thinking_start";
	contentIndex: number;
	partial: AssistantMessage;
} | {
	type: "thinking_delta";
	contentIndex: number;
	delta: string;
	partial: AssistantMessage;
} | {
	type: "thinking_end";
	contentIndex: number;
	content: string;
	partial: AssistantMessage;
} | {
	type: "image_end";
	contentIndex: number;
	content: ImageContent;
	partial: AssistantMessage;
} | {
	type: "toolcall_start";
	contentIndex: number;
	partial: AssistantMessage;
} | {
	type: "toolcall_delta";
	contentIndex: number;
	delta: string;
	partial: AssistantMessage;
} | {
	type: "toolcall_end";
	contentIndex: number;
	toolCall: ToolCall;
	partial: AssistantMessage;
} | {
	type: "done";
	contentIndex?: undefined;
	reason: Extract<StopReason, "stop" | "length" | "toolUse">;
	message: AssistantMessage;
} | {
	type: "error";
	contentIndex?: undefined;
	reason: Extract<StopReason, "aborted" | "error">;
	error: AssistantMessage;
};
interface AnthropicFallbackCreditHandle {
	token: string;
	prefillClaim?: boolean | null;
	/** The refused request's body, replayed with the credit token on the fallback model. */
	params: MessageCreateParamsStreaming;
	betas?: readonly string[];
	betaHeader?: string;
	expiresAt: number;
	/** The refused response's content, in `AssistantMessage` block form. */
	refusedContent?: AssistantMessage["content"];
}
interface ToolCounters {
	readonly total: number;
	readonly ok: number;
	readonly error: number;
	readonly skipped: number;
	readonly blocked: number;
	readonly timeout: number;
	readonly aborted: number;
	readonly totalLatencyMs: number;
}
interface AgentRunSummary {
	readonly chats: {
		readonly total: number;
		/** Bucketed by raw {@link StopReason}; absent reasons omitted. */
		readonly byStopReason: Readonly<Record<string, number>>;
		readonly totalLatencyMs: number;
	};
	readonly tools: {
		readonly total: number;
		readonly ok: number;
		readonly error: number;
		readonly skipped: number;
		readonly blocked: number;
		readonly timeout: number;
		readonly aborted: number;
		readonly totalLatencyMs: number;
		/** Per-tool-name counters; keys sorted by name on snapshot. */
		readonly byName: Readonly<Record<string, ToolCounters>>;
	};
	readonly usage: {
		readonly inputTokens: number;
		readonly outputTokens: number;
		readonly cachedInputTokens: number;
		readonly cacheWriteTokens: number;
		readonly reasoningOutputTokens: number;
		readonly totalTokens: number;
	};
	readonly cost: {
		readonly estimatedUsd: number;
		/** Sorted, deduped. */
		readonly unavailableReasons: readonly string[];
	};
	readonly errors: {
		readonly total: number;
		readonly byType: Readonly<Record<string, number>>;
	};
	readonly stepCount: number;
}
interface AgentRunCoverage {
	readonly toolsAvailable: readonly string[];
	readonly toolsInvoked: readonly string[];
	readonly toolsUnused: readonly string[];
	readonly modelsUsed: readonly string[];
	readonly providersUsed: readonly string[];
}
interface CustomAgentMessages {
}
type AgentMessage = Message | CustomAgentMessages[keyof CustomAgentMessages];
interface AgentToolResult<T = any, _TInput = unknown> {
	content: (TextContent | ImageContent)[];
	details?: T;
	isError?: boolean;
	/** Provider-native metadata that must survive into history replay unchanged. */
	providerMetadata?: ToolResultProviderMetadata;
	/** Marks the result as contextually useless: safe for compaction to elide once consumed (e.g. zero matches, wait timeout). Ignored when isError is set. */
	useless?: boolean;
}
type ToolLoadMode = "essential" | "discoverable";
type AgentEvent = {
	type: "agent_start";
	/**
	 * Calls of the unpaired assistant tail a continued run executes before its
	 * first model request. They run without a new assistant `message_end`.
	 */
	resumedToolCallIds?: string[];
} | {
	type: "agent_end";
	messages: AgentMessage[];
	/** Present iff `AgentTelemetryConfig` was supplied on this run. */
	telemetry?: AgentRunSummary;
	coverage?: AgentRunCoverage;
} | {
	type: "turn_start";
} | {
	type: "turn_end";
	message: AgentMessage;
	toolResults: ToolResultMessage[];
} | {
	type: "message_start";
	message: AgentMessage;
} | {
	type: "message_update";
	message: AgentMessage;
	assistantMessageEvent: AssistantMessageEvent;
} | {
	type: "message_end";
	message: AgentMessage;
} | {
	type: "tool_execution_start";
	toolCallId: string;
	toolName: string;
	args: any;
	intent?: string;
} | {
	type: "tool_execution_update";
	toolCallId: string;
	toolName: string;
	args: any;
	partialResult: any;
} | {
	type: "tool_stream_update";
	toolCallId: string;
	toolName: string;
	update: unknown;
} | {
	type: "tool_execution_end";
	toolCallId: string;
	toolName: string;
	result: any;
	isError?: boolean;
};
interface CustomMessage<T = unknown> {
	role: "custom";
	customType: string;
	content: string | (TextContent | ImageContent)[];
	display: boolean;
	details?: T;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	timestamp: number;
}
interface HookMessage<T = unknown> {
	role: "hookMessage";
	customType: string;
	content: string | (TextContent | ImageContent)[];
	display: boolean;
	details?: T;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	timestamp: number;
}
interface BranchSummaryMessage {
	role: "branchSummary";
	summary: string;
	fromId: string;
	timestamp: number;
}
interface CompactionSummaryMessage {
	role: "compactionSummary";
	summary: string;
	shortSummary?: string;
	tokensBefore: number;
	/** Estimated context tokens after the rewrite (display metadata). */
	tokensAfter?: number;
	/** Harness compaction method that produced this summary (display metadata). */
	method?: string;
	providerPayload?: ProviderPayload;
	/** Runtime-only ordered archive blocks for snapcompact: old text region,
	 *  imaged middle, then new text region. When present, `summary` is already
	 *  the final lead-in text (no legacy wrapper applied). */
	blocks?: (TextContent | ImageContent)[];
	/** Snapcompact image blocks, kept for display counts / legacy consumers. */
	images?: ImageContent[];
	/** Post-pass dead-end warning attached to this compaction (progress guard). */
	warning?: string;
	/**
	 * Thinking-binding rewrite marker when it must differ from `timestamp`: a
	 * natively replayed summary predates it before the retained tail so that
	 * tail's bound thinking stays valid. `timestamp` remains the commit time,
	 * which is what invalidates the tail's pre-compaction usage reports.
	 */
	historyRewriteAt?: number;
	timestamp: number;
}
interface CustomAgentMessages {
	custom: CustomMessage;
	hookMessage: HookMessage;
	branchSummary: BranchSummaryMessage;
	compactionSummary: CompactionSummaryMessage;
}
declare const ThinkingLevel: {
	readonly Inherit: "inherit";
	readonly Off: "off";
	readonly Minimal: Effort.Minimal;
	readonly Low: Effort.Low;
	readonly Medium: Effort.Medium;
	readonly High: Effort.High;
	readonly XHigh: Effort.XHigh;
	readonly Max: Effort.Max;
};
type ThinkingLevel = (typeof ThinkingLevel)[keyof typeof ThinkingLevel];
interface CompactionResult<T = unknown> {
	summary: string;
	/** Short PR-style summary for display purposes. */
	shortSummary?: string;
	firstKeptEntryId: string;
	tokensBefore: number;
	/** Hook-specific data (e.g., ArtifactIndex, version markers for structured compaction) */
	details?: T;
	/** Hook-provided data to persist alongside compaction entry. */
	preserveData?: Record<string, unknown>;
	/**
	 * Last entry the provider-native replay history covers. Set when a remote
	 * compaction could only fit the history before the kept tail; later entries
	 * replay normally after the native history.
	 */
	providerReplayThroughEntryId?: string;
}
type ShellTraceBytes = string | number[];
type ShellTraceNodeKind = "and_or" | "pipeline" | "simple" | "brace" | "subshell" | "if" | "while" | "until" | "for" | "arith_for" | "select" | "case" | "arith" | "cond" | "func_def" | "coproc" | "assign" | "word";
type ShellTraceResolved = "function" | "intrinsic" | "tool" | "native" | "device" | "self_shell" | "external" | "not_found" | "none";
type ShellTraceState = "pending" | "running" | "ok" | "fail" | "skipped";
interface ShellTraceRedir {
	fd: number;
	op: string;
	target: ShellTraceBytes;
}
interface ShellTraceDiag {
	code: string;
	message: ShellTraceBytes;
}
interface ShellTraceNode {
	k: ShellTraceNodeKind;
	/** Byte span relative to the unit source. */
	lo: number;
	hi: number;
	/** Parent node id; absent for the root. */
	p?: number;
	state: ShellTraceState;
	/** CmdStart count. */
	runs?: number;
	/** CmdStart − CmdEnd. */
	live?: number;
	/** Status of the last CmdEnd. */
	st?: number;
	/** CmdEnds with a non-zero status. */
	fails?: number;
	/** Sum of `dur_ns` over the ended runs. */
	ns?: number;
	/** `t_ns` of the last CmdStart, relative to RunStart. */
	at?: number;
	/** Last CmdExpanded argv (each arg ≤ 256 bytes, ≤ 64 args). */
	argv?: ShellTraceBytes[];
	/** The full argv length when `argv` was cut. */
	argc?: number;
	/** Last CmdExpanded assigns. */
	env?: [
		ShellTraceBytes,
		ShellTraceBytes
	][];
	res?: ShellTraceResolved;
	redirs?: ShellTraceRedir[];
	/** Pipestatus of the last CmdEnd. */
	pipe?: number[];
	/** Last IterStart n. */
	iter?: number;
	/** LoopEnd iterations. */
	iters?: number;
	/** Last Branch decision. */
	taken?: boolean;
	/** A Background event named this node. */
	bg?: boolean;
	/** Last Spawn pid. */
	pid?: number;
	/** Actual last command completion timestamp and trace revision. */
	end_ns?: number;
	end_seq?: number;
}
interface ShellTraceUnit {
	/** Byte offset of the unit inside `script`; absent when the unit is not part of the script. */
	at?: number;
	/** 1-based script line. */
	line: number;
	/** Exact UTF-8 source byte length, including heredocs; absent in older traces. */
	bytes?: number;
	/** The unit's source; present only for unlocated units (an eval or function body). */
	source?: ShellTraceBytes;
	/** Opaque canonical native syntax archive. Only pi-shell may interpret it. */
	syntax?: unknown;
	/** Compiler-known wrapper bytes removed from this unit's displayed source. */
	projection?: number;
	/** Script a callout (`timeout`, `git`, `xargs`, …) ran for its operands; its calling command stands for it. */
	callout?: boolean;
	nodes: ShellTraceNode[];
}
interface ShellTraceCommandTag {
	/** Actual engine field: descendant vprocs without a root run queue use 0.
	 * Do not reject those tags; capture generation and explicit fork events
	 * establish run ownership, while vpid/seq distinguish the producer. */
	run: number;
	vpid: number;
	seq: number;
}
interface ShellTraceOutputContributor {
	t_ns: number;
	bytes: number[];
	command?: ShellTraceCommandTag;
	vpid?: number;
	u?: number;
	n?: number;
}
interface ShellTraceHistoryEvent {
	/** Trace revision, including coalesced output growth. */
	seq: number;
	/** Measured monotonic engine/host nanoseconds since RunStart, like node.at. */
	t_ns: number;
	/** Original engine event sequence; absent for Output callbacks. */
	event_seq?: number;
	/** Zero-based actual minimizer segment submission; lifecycle entries remain real engine events. */
	segment?: number;
	/** The last real root RunEnd finalized the entire logical segmented call. */
	logicalEnd?: boolean;
	/** Actual host outcome after output/close draining, without replacing engine status. */
	logicalStatus?: number;
	/** Actual emitting vproc, present on semantic entries even without a command tag. */
	vpid?: number;
	kind: "CmdStart" | "CmdExpanded" | "CmdEnd" | "Pipeline" | "Branch" | "IterStart" | "LoopEnd" | "Background" | "FuncEnter" | "FuncExit" | "Assign" | "RunStart" | "RunEnd" | "Output" | "Diag" | "Exit" | "ErrExit" | "Spawn";
	u?: number;
	n?: number;
	command?: ShellTraceCommandTag;
	status?: number;
	dur_ns?: number;
	pipestatus?: number[];
	argv?: ShellTraceBytes[];
	assigns?: [
		ShellTraceBytes,
		ShellTraceBytes
	][];
	resolved?: ShellTraceResolved;
	redirs?: ShellTraceRedir[];
	name?: ShellTraceBytes;
	value?: ShellTraceBytes;
	full_len?: number;
	scope?: string;
	iteration?: number;
	iterations?: number;
	taken?: boolean;
	construct?: string;
	/** `Output` text is always a string (undecodable bytes arrive as U+FFFD); `Diag` text is run bytes. */
	text?: ShellTraceBytes;
	contributors?: ShellTraceOutputContributor[];
	/** Final visible capture descriptor; merged stderr uses stdout's descriptor. */
	fd?: number;
	pid?: number;
}
interface ShellTrace {
	v: 1;
	/** Semantic/output revision, including assignments/coalesced bytes; monotonic poll key. */
	seq: number;
	/** Root RunEnd status; absent while running. */
	status?: number;
	/** Actual root vproc, even if history trimming removed its RunStart. */
	rootVpid?: number;
	/** Run clock (ns since RunStart) at the last folded event. */
	t_ns: number;
	/** The command text given at construction. */
	script: string;
	/** Compiled units, in announce order. */
	units: ShellTraceUnit[];
	/** Newest ≤20,000 semantic/output records, ≤2 MiB visible output and ≤8 MiB retained payload.
	 * Adjacent output from the same execution may coalesce; its seq changes
	 * when text grows. Consumers must replace the full journal on revision. */
	history?: ShellTraceHistoryEvent[];
	/** True when any replay record or output byte was evicted. */
	historyTruncated?: boolean;
	/** The last ≤ 8 diagnostics. */
	diags?: ShellTraceDiag[];
}
type TspTone = "neutral" | "accent" | "info" | "success" | "warning" | "error" | "pending" | "muted" | "user";
type LivePhase = "connecting" | "listening" | "working" | "speaking" | "muted" | "error";
type FormSpanStyle = "code" | "strong" | "em";
interface FormSpan {
	text: string;
	style?: FormSpanStyle;
}
interface ChoiceOption {
	value: string;
	label: FormSpan[];
	checked: boolean;
	disabled: boolean;
}
interface ChoiceField {
	kind: "choice";
	name: string;
	multi: boolean;
	/** From a `<select>`: a single select draws as a one-row dropdown instead of option rows. */
	select: boolean;
	/**
	 * The UI adds a free-text "Other" answer: set for radio and checkbox groups of
	 * two or more options that don't already offer one of their own.
	 */
	other: boolean;
	/** The control's own label; empty for radio/checkbox groups, whose question is the surrounding text. */
	label: FormSpan[];
	options: ChoiceOption[];
	required: boolean;
}
interface TextField {
	kind: "text";
	name: string;
	/** The `<input type>` (`text`, `number`, `url`, …); `textarea` for textareas. */
	type: string;
	multiline: boolean;
	label: FormSpan[];
	value: string;
	placeholder: string;
	required: boolean;
	min?: number;
	max?: number;
	pattern?: string;
	minLength?: number;
	maxLength?: number;
	/** Visible rows a textarea asked for (`rows`), 1 for inputs. */
	rows: number;
}
type FormField = ChoiceField | TextField;
type FormBlock = {
	kind: "text";
	spans: FormSpan[];
	heading: boolean;
} | {
	kind: "list";
	items: FormSpan[][];
	ordered: boolean;
} | {
	kind: "pre";
	text: string;
} | {
	kind: "table";
	rows: FormSpan[][][];
	headerRows: number;
}
/** An `<img>`: `src` is an http(s) or `data:` URL, a `file:` URL or a local path (see `media.ts`). */
 | {
	kind: "image";
	src: string;
	alt: string;
}
/** An inline `<svg>`, serialized whole. */
 | {
	kind: "svg";
	markup: string;
	alt: string;
}
/**
 * A field. A radio/checkbox group whose later options come after other blocks of its section
 * (text between options) has `optionsEnd`: the index just past the last of those blocks.
 */
 | {
	kind: "field";
	field: FormField;
	optionsEnd?: number;
};
interface CopyTarget {
	id: string;
	label: string;
	hint?: string;
	preview: string;
	language?: string;
	content?: string;
	copyMessage?: string;
	children?: CopyTarget[];
}
interface LogoutAccount {
	credentialId: number;
	provider: string;
	label: string;
	detail: string;
	type: "api_key" | "oauth";
	active: boolean;
}
type LoopLimitRuntime = {
	kind: "iterations";
	initial: number;
	remaining: number;
} | {
	kind: "duration";
	durationMs: number;
	deadlineMs: number;
};
type ConfiguredThinkingLevel = ThinkingLevel | "auto";
type GoalStatus = "active" | "paused" | "budget-limited" | "complete" | "dropped";
interface Goal {
	id: string;
	objective: string;
	status: GoalStatus;
	tokenBudget?: number;
	tokensUsed: number;
	timeUsedSeconds: number;
	createdAt: number;
	updatedAt: number;
}
type AgentSource = "bundled" | "user" | "project";
type StructuredSubagentSchemaMode = "permissive" | "strict";
type StructuredSubagentSchemaSource = "caller" | "agent" | "session" | "none";
type StructuredSubagentValidationStatus = "valid" | "invalid" | "unavailable";
interface StructuredSubagentOutput {
	source: StructuredSubagentSchemaSource;
	mode: StructuredSubagentSchemaMode;
	status: StructuredSubagentValidationStatus;
	data?: unknown;
	error?: string;
}
interface AgentProgress {
	index: number;
	id: string;
	agent: string;
	agentSource: AgentSource;
	status: "pending" | "running" | "completed" | "failed" | "aborted";
	task: string;
	assignment?: string;
	description?: string;
	lastIntent?: string;
	currentTool?: string;
	currentToolArgs?: string;
	/** Argument key selected for the display preview, when known. */
	currentToolArgsKey?: string;
	/** Intent the model attached to the current call; undefined when that call carried none. */
	currentToolIntent?: string;
	currentToolStartMs?: number;
	recentTools: Array<{
		tool: string;
		args: string;
		argsKey?: string;
		intent?: string;
		isError?: boolean;
		endMs: number;
	}>;
	recentOutput: string[];
	toolCount: number;
	/** Count of assistant requests (assistant message_end events) across the run. Drives the soft request budget guard. */
	requests: number;
	/** Cumulative input + output + cacheWrite tokens across all turns. Excludes cacheRead (re-reads cached context every turn, making cumulative sum misleading). */
	tokens: number;
	/**
	 * Current per-turn context size: latest assistant message's `usage.totalTokens`.
	 * This is the number to compare against `contextWindow` — what compaction
	 * decides on, what the user typically reads as "how full is the context".
	 * Distinct from `tokens`, which is a lifetime billing-volume counter.
	 */
	contextTokens?: number;
	/** Model's context window in tokens, when known. Lets the UI render `<curr>/<window>` gauges. */
	contextWindow?: number;
	/** Cumulative billing cost in USD, accumulated incrementally from message_end events. */
	cost: number;
	durationMs: number;
	modelOverride?: string | string[];
	/** Explicit pre-expansion model role alias selected for this run. */
	modelRole?: string;
	/** Resolved model display string in the form `<provider>/<id>`, optionally suffixed with `:<thinkingLevel>` when the level was set explicitly. Undefined when the model could not be resolved. */
	resolvedModel?: string;
	/** Provider/id including routing, with no added thinking suffix. */
	resolvedModelIdentity?: string;
	/** Explicit thinking metadata; never inferred from the model identity. */
	resolvedThinkingLevel?: ConfiguredThinkingLevel;
	/** True when {@link resolvedModel} is the target of an active retry fallback (not the originally configured model). Lets observer-only UIs (collab guests, Agent Hub rows with no live session) flag the fallback and keep the provider. */
	resolvedModelIsFallback?: boolean;
	/** Extension routing note (e.g. model-pools) explaining why {@link resolvedModel} was chosen. */
	resolvedModelRoute?: string;
	/** True when a live advisor was attached to this run's session, not merely enabled in settings. */
	advisor?: boolean;
	/** The agent's latest self-estimate of task completion (0–100), from the periodic `task.completionProbe` side request. */
	completionPercent?: number;
	/**
	 * The agent's current step in its own words (one line, ≤ 80 chars), from the
	 * same probe reply as {@link completionPercent}. Running rows prefer it over
	 * `lastIntent`; the job widget uses it as its stage line.
	 */
	stage?: string;
	/**
	 * What the agent says shows its progress live (one line, ≤ 200 chars), e.g.
	 * "BENCH lines in bg_2 pipe:1, tok_s higher is better, target 70% of roofline";
	 * from the probe reply. Feeds the job widget author's brief; never rendered.
	 */
	watch?: string;
	/** Data extracted by registered subprocess tool handlers (keyed by tool name) */
	extractedToolData?: Record<string, unknown[]>;
	/**
	 * Auto-retry state when the subagent is sleeping between provider retries
	 * (e.g. 429 rate-limit with retry-after). Cleared when the retry resolves
	 * or fails. Surfacing this to the parent prevents the task tool from
	 * looking indefinitely "in progress" when a child is actually blocked on
	 * provider quota.
	 */
	retryState?: {
		attempt: number;
		maxAttempts: number;
		delayMs: number;
		errorMessage: string;
		startedAtMs: number;
	};
	/**
	 * Terminal retry failure surfaced once the subagent gave up retrying
	 * (e.g. retry-after exceeded the cap, or all attempts exhausted). Carries
	 * the final error so the parent UI can render "blocked: rate-limited"
	 * instead of waiting for a status that never arrives.
	 */
	retryFailure?: {
		attempt: number;
		errorMessage: string;
	};
	/**
	 * Snapshot of the most recent `task` tool call's in-flight `TaskToolDetails`,
	 * captured from `tool_execution_update`. Lets the parent UI surface live
	 * nested-subagent progress while this agent is still inside its own `task`
	 * call. Cleared when the call ends — finalized data lives in
	 * `extractedToolData.task` after that.
	 */
	inflightTaskDetails?: TaskToolDetails;
}
interface SingleResult {
	index: number;
	id: string;
	agent: string;
	agentSource: AgentSource;
	task: string;
	assignment?: string;
	description?: string;
	lastIntent?: string;
	exitCode: number;
	output: string;
	stderr: string;
	truncated: boolean;
	/**
	 * Parsed structured completion and validation metadata, when this invocation
	 * selected an output schema or strict schema mode.
	 */
	structuredOutput?: StructuredSubagentOutput;
	durationMs: number;
	/** Cumulative input + output + cacheWrite tokens across all turns. Excludes cacheRead (re-reads cached context every turn, making cumulative sum misleading). */
	tokens: number;
	/** Count of assistant requests (assistant message_end events) across the run. */
	requests: number;
	/** Latest per-turn context size at task completion. See `AgentProgress.contextTokens`. */
	contextTokens?: number;
	/** Model's context window in tokens, when known. */
	contextWindow?: number;
	modelOverride?: string | string[];
	/** Explicit pre-expansion model role alias selected for this run. */
	modelRole?: string;
	/** Resolved model display string in the form `<provider>/<id>`, optionally suffixed with `:<thinkingLevel>` when the level was set explicitly. Omitted from tool-result JSON when undefined to keep wire payloads small. */
	resolvedModel?: string;
	/** Retains the unambiguous identity from {@link AgentProgress.resolvedModelIdentity}. */
	resolvedModelIdentity?: string;
	/** Retains {@link AgentProgress.resolvedThinkingLevel} after settlement. */
	resolvedThinkingLevel?: ConfiguredThinkingLevel;
	/** True when {@link resolvedModel} is the target of an active retry fallback. Mirrors {@link AgentProgress.resolvedModelIsFallback} onto the settled result. */
	resolvedModelIsFallback?: boolean;
	/** Mirrors {@link AgentProgress.resolvedModelRoute} onto the settled result. */
	resolvedModelRoute?: string;
	/** Retains {@link AgentProgress.advisor} after the advised session is disposed. */
	advisor?: boolean;
	error?: string;
	aborted?: boolean;
	abortReason?: string;
	/** Aggregated usage from the subprocess, accumulated incrementally from message_end events. */
	usage?: Usage;
	/** Output path for the task result */
	outputPath?: string;
	/**
	 * Ran inside an isolation worktree. Such agents are parked without a
	 * reviver once the worktree is torn down, so they are never resumable or
	 * messageable after the run — summaries must not suggest otherwise.
	 */
	isolated?: boolean;
	/** Patch path for isolated worktree output */
	patchPath?: string;
	/**
	 * Whether `patchPath` holds a non-empty root-repo diff. `false` when the
	 * agent's changes all live in nested repos (see `nestedPatchPaths`), so
	 * summaries do not claim the root patch captured anything.
	 */
	hasRootChanges?: boolean;
	/** Branch name for isolated branch-mode output */
	branchName?: string;
	/**
	 * Baseline commit SHA the task branch was created from. Passed to
	 * `mergeTaskBranches` so cherry-pick uses the inclusive range
	 * `branchBaseSha..branchName` and preserves every agent commit's message.
	 */
	branchBaseSha?: string;
	/** Nested repo patches to apply after parent merge */
	nestedPatches?: NestedRepoPatch[];
	/**
	 * On-disk copies of `nestedPatches`, one file per nested repo, written
	 * before the isolation workspace is torn down. The workspace is the only
	 * other copy of that work, so these paths are the durable record.
	 */
	nestedPatchPaths?: string[];
	/** Data extracted by registered subprocess tool handlers (keyed by tool name) */
	extractedToolData?: Record<string, unknown[]>;
	/**
	 * Terminal retry failure, when the subagent exited because the auto-retry
	 * loop gave up (retry-after exceeded the cap, or all attempts exhausted).
	 * Lets the parent task tool surface a "blocked: rate-limited" outcome
	 * instead of a generic failure.
	 */
	retryFailure?: {
		attempt: number;
		errorMessage: string;
	};
	/** Output metadata for agent:// URL integration */
	outputMeta?: {
		lineCount: number;
		charCount: number;
	};
}
interface TaskToolDetails {
	projectAgentsDir: string | null;
	results: SingleResult[];
	totalDurationMs: number;
	/** Aggregated usage across all subagents. */
	usage?: Usage;
	outputPaths?: string[];
	progress?: AgentProgress[];
	async?: {
		state: "running" | "completed" | "failed";
		jobId: string;
		type: "task";
	};
}
interface NestedRepoPatch {
	relativePath: string;
	patch: string;
}
type OutputArtifactError = "open" | "write" | "flush" | "end";
interface OutputSummary {
	output: string;
	truncated: boolean;
	totalLines: number;
	totalBytes: number;
	outputLines: number;
	outputBytes: number;
	/** Bytes elided from the middle when head-retain mode is active. */
	elidedBytes?: number;
	/** Lines elided from the middle when head-retain mode is active. */
	elidedLines?: number;
	/** Bytes dropped by the per-line column cap (sum across all lines). */
	columnDroppedBytes?: number;
	/** Number of distinct lines that hit the per-line column cap. */
	columnTruncatedLines?: number;
	/**
	 * 1-indexed first and last lines the column cap cut, numbered as in the
	 * artifact. Absent when the numbering cannot match the artifact (an inline
	 * substitute was capped, or `replace()` rewrote the buffer).
	 */
	columnTruncatedRange?: {
		first: number;
		last: number;
	};
	/** Configured per-line column cap in effect (UTF-8 bytes), when > 0. */
	columnMax?: number;
	/** Artifact ID for internal URL access (artifact://<id>) when truncated */
	artifactId?: string;
	/**
	 * Bytes the artifact cap dropped from the middle of the saved file. When
	 * set, `artifact://<id>` holds a head/tail sample, not the full output.
	 */
	artifactElidedBytes?: number;
	/** Full raw output was not completely saved; artifactId is unavailable. */
	artifactError?: OutputArtifactError;
}
interface TruncationMeta {
	direction: "head" | "tail" | "middle";
	truncatedBy: "lines" | "bytes" | "middle";
	totalLines: number;
	totalBytes: number;
	outputLines: number;
	outputBytes: number;
	maxBytes?: number;
	/** Line range shown (1-indexed, inclusive). Omitted for middle elision. */
	shownRange?: {
		start: number;
		end: number;
	};
	/** Head/tail line ranges shown when direction === "middle". */
	headRange?: {
		start: number;
		end: number;
	};
	tailRange?: {
		start: number;
		end: number;
	};
	/** Bytes elided from the middle. */
	elidedBytes?: number;
	/** Lines elided from the middle. */
	elidedLines?: number;
	/** Artifact ID if full output was saved */
	artifactId?: string;
	/** Bytes the artifact cap dropped from the saved file's middle; the artifact is then a head/tail sample. */
	artifactElidedBytes?: number;
	/** Next offset for pagination (head truncation only) */
	nextOffset?: number;
	/**
	 * The single shown line is a byte-capped preview of one oversized line, not
	 * a complete line. `outputBytes`/`totalBytes` are the preview vs full line
	 * size; renders a distinct "partial" notice instead of a line range.
	 */
	partialLine?: boolean;
}
type SourceMeta = {
	type: "path";
	value: string;
} | {
	type: "url";
	value: string;
} | {
	type: "internal";
	value: string;
}
/** A complete aggregate report, whose entries may contain incomplete source captures. */
 | {
	type: "report";
	value: string;
};
interface DiagnosticMeta {
	summary: string;
	messages: string[];
}
interface LimitsMeta {
	matchLimit?: {
		reached: number;
		suggestion: number;
	};
	/** `suggestion` is omitted when the tool is already at its hard cap, so no larger usable limit exists to advise. */
	resultLimit?: {
		reached: number;
		suggestion?: number;
	};
	headLimit?: {
		reached: number;
		suggestion: number;
	};
	/**
	 * `unit` may be absent in sessions persisted before it was recorded. `lines` is
	 * the first-to-last range of cut lines, and `selectorBase` the target a
	 * `:raw:<line>-<line>` selector reads one of them whole from (an artifact's
	 * `artifact://<id>` stands in when absent).
	 */
	columnTruncated?: {
		maxColumn: number;
		unit?: "bytes" | "chars";
		artifactId?: string;
		artifactElidedBytes?: number;
		lines?: {
			first: number;
			last: number;
		};
		selectorBase?: string;
		/** The first cut line exceeds what one `:raw` read returns; that read stops at this many bytes. */
		rawByteCap?: number;
	};
}
interface MinimizedMeta {
	filter: string;
	inputBytes: number;
	outputBytes: number;
	artifactId: string;
}
interface OutputMeta {
	truncation?: TruncationMeta;
	/** Capture failure of this output itself; aggregate reports keep source failures on their entries. */
	artifactError?: OutputArtifactError;
	source?: SourceMeta;
	/** The output is a bounded page of a file or session artifact its `source` re-reads with line selectors; spill-to-artifact skips it. */
	pagedSource?: true;
	diagnostics?: DiagnosticMeta;
	limits?: LimitsMeta;
	minimized?: MinimizedMeta;
}
interface CustomAgentMessages {
	bashExecution: BashExecutionMessage;
	pythonExecution: PythonExecutionMessage;
	custom: CustomMessage$1;
	hookMessage: HookMessage$1;
	branchSummary: BranchSummaryMessage;
	compactionSummary: CompactionSummaryMessage;
	fileMention: FileMentionMessage;
}
type CustomMessageContent = string | (TextContent | ImageContent)[];
interface BashExecutionMessage {
	role: "bashExecution";
	command: string;
	output: string;
	exitCode: number | undefined;
	cancelled: boolean;
	truncated: boolean;
	/** Images extracted from terminal graphics in the command's stdout. */
	images?: ImageContent[];
	meta?: OutputMeta;
	timestamp: number;
	/** If true, this message is excluded from LLM context (!! prefix) */
	excludeFromContext?: boolean;
}
interface PythonExecutionMessage {
	role: "pythonExecution";
	code: string;
	output: string;
	exitCode: number | undefined;
	cancelled: boolean;
	truncated: boolean;
	meta?: OutputMeta;
	timestamp: number;
	/** If true, this message is excluded from LLM context ($ prefix) */
	excludeFromContext?: boolean;
}
interface CustomMessage$1<T = unknown> {
	role: "custom";
	customType: string;
	content: CustomMessageContent;
	display: boolean;
	details?: T;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	timestamp: number;
}
interface HookMessage$1<T = unknown> {
	role: "hookMessage";
	customType: string;
	content: CustomMessageContent;
	display: boolean;
	details?: T;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
	timestamp: number;
}
interface FileMentionMessage {
	role: "fileMention";
	files: Array<{
		path: string;
		content: string;
		lineCount?: number;
		/** File size in bytes, if known. */
		byteSize?: number;
		/** Why the file contents were omitted from auto-read. */
		skippedReason?: "tooLarge" | "binary";
		image?: ImageContent;
	}>;
	timestamp: number;
}
type TodoStatus = "pending" | "in_progress" | "completed" | "abandoned";
interface TodoItem {
	content: string;
	status: TodoStatus;
	details?: string;
	notes?: string[];
}
interface TodoPhase {
	name: string;
	tasks: TodoItem[];
}
type ProviderDiscoveryStatus = "idle" | "ok" | "empty" | "cached" | "unavailable" | "unauthenticated";
interface ProviderDiscoveryState {
	provider: string;
	status: ProviderDiscoveryStatus;
	optional: boolean;
	stale: boolean;
	fetchedAt?: number;
	source?: ModelResolutionSource;
	models: string[];
	error?: string;
}
interface BashResult extends OutputSummary {
	exitCode: number | undefined;
	cancelled: boolean;
	/** True when the command was killed by its timeout deadline (not a user abort). */
	timedOut?: boolean;
	/** The final trace snapshot of an embedded-shell run started with `onTrace`. */
	trace?: ShellTrace;
	/** The native minimizer's rewrite, set when its output replaced the raw stream. */
	minimized?: MinimizedMeta;
	workingDir?: string;
	/** Terminal graphics extracted from raw stdout before sanitization or truncation. */
	images?: ImageContent[];
}
type JobWidgetState = 
/** The cheap model is writing the spec; the view holds the model-free fallback meanwhile. */
"authoring"
/** The spec's rules drive the view. */
 | "live"
/** No spec (no model, authoring failed, nothing to read yet): last line and rates only. */
 | "fallback";
interface JobWidgetTrend {
	/** `▲ 15%`, `▼ 3.2 ms`. */
	text: string;
	/** `success` when the move is toward `better`, `error` away from it, `muted` without a direction. */
	tone: "success" | "error" | "muted";
}
interface JobWidgetHeadline {
	/** Formatted value with its unit. */
	text: string;
	/** What the value is, when the title does not already say it (`decode`, `p50`). */
	label?: string;
	trend?: JobWidgetTrend;
}
interface JobWidgetSparkBlock {
	kind: "spark";
	/** What the series measures (`tok/s · nvfp4 b1`). */
	label: string;
	/** At most 48 values, oldest first, already downsampled. */
	values: number[];
	/** Domain of the chart; covers the values and every mark. */
	min: number;
	max: number;
	/** Horizontal reference lines in value units (a target, a baseline). */
	marks?: {
		value: number;
		label: string;
		tone?: TspTone;
	}[];
	/** Latest value, formatted (`186 tok/s`). */
	last: string;
	/** Per-point tooltip text, same length as `values` (`12:04:31 · 186 tok/s`). */
	tips?: string[];
	tone?: TspTone;
}
interface JobWidgetBarsBlock {
	kind: "bars";
	label: string;
	items: {
		label: string;
		value: number;
		text: string;
		tone?: TspTone;
	}[];
	/** The value a full bar stands for. */
	max: number;
}
interface JobWidgetMeterBlock {
	kind: "meter";
	label: string;
	/** 0–1. */
	value: number;
	/** `7/9 trials`. */
	text: string;
	tone?: TspTone;
}
interface JobWidgetStatBlock {
	kind: "stat";
	items: {
		label: string;
		text: string;
		tone?: TspTone;
		trend?: JobWidgetTrend;
	}[];
}
interface JobWidgetGridBlock {
	kind: "grid";
	label: string;
	items: {
		label: string;
		text: string;
		tone: TspTone;
	}[];
}
interface JobWidgetTextBlock {
	kind: "text";
	text: string;
}
type JobWidgetBlock = JobWidgetSparkBlock | JobWidgetBarsBlock | JobWidgetMeterBlock | JobWidgetStatBlock | JobWidgetGridBlock | JobWidgetTextBlock;
interface JobWidgetView {
	/** Short name of what is being watched (`loadtest · 3 models`). */
	title: string;
	headline?: JobWidgetHeadline;
	/** What the job is doing now: a subagent's stage, or the last line of output. */
	stage?: string;
	/** Top to bottom; the first block is the primary one, and compact views show only it. */
	blocks: JobWidgetBlock[];
	state: JobWidgetState;
	/** Epoch ms of the newest data the view reflects. */
	updatedAt: number;
}
interface ContextUsage {
	/** Estimated context tokens. */
	tokens: number;
	contextWindow: number;
	/** Context usage as percentage of context window. */
	percent: number;
}
interface JobTextChunk {
	/** Offset of `text`'s first unit; past the requested `from` when older text was dropped. */
	start: number;
	/** Offset just past `text`: the next read's `from`. */
	end: number;
	text: string;
	/** Whether more text may arrive. */
	open: boolean;
}
interface JobTextSource {
	/** Stable id a widget rule names in `from`: `out`, `pipe:1`, `tools`, `bg_7:pipe:1`. */
	readonly id: string;
	/** What the text is, for the widget author (`cargo nextest run … → tail -n 5`). */
	readonly label: string;
	read(from: number): JobTextChunk;
}
interface JobWidgetBrief {
	kind: "bash" | "task";
	/** bash: the full command line. */
	command?: string;
	/** bash: seconds until the command is killed; absent when it has no timeout. */
	timeoutSec?: number;
	/** task: the assignment the subagent was given. */
	task?: string;
	/** The model's intent line on the call that started the job. */
	intent?: string;
	/** task: what the subagent says is worth watching, from its completion probe. */
	watch?: string;
	/** task: the subagent's current stage, from its completion probe. */
	stage?: string;
}
interface JobWidgetInputs {
	/** The job's text sources now; a task's list grows as its subagent starts jobs. */
	sources(): readonly JobTextSource[];
	brief(): JobWidgetBrief;
}
type AsyncJobType = "bash" | "task" | "eval";
interface AsyncJobDetails extends Record<string, unknown> {
	/** Images recovered from command output, independent of text truncation. */
	images?: ImageContent[];
	/** Tool output metadata needed when a completion is delivered or recovered later. */
	meta?: OutputMeta;
}
interface AsyncJob {
	id: string;
	type: AsyncJobType;
	status: "running" | "completed" | "failed" | "cancelled";
	startTime: number;
	/** When the job's run settled; `endTime - startTime` is its frozen run duration. */
	endTime?: number;
	/** Compatibility timestamp set after the job body has fully settled, including cancellation. */
	endedAt?: number;
	label: string;
	/**
	 * What the job is for, in the model's words: the `i` intent on the call
	 * that started it, as one line of plain text. Job views lead with it over
	 * the `label`. Only tools whose call starts a single job set it.
	 */
	intent?: string;
	/**
	 * The user's request a `/tan` job carries out, verbatim. The owner never saw
	 * that work, so its completion notice quotes the request and frames the
	 * result as the user's tangent rather than the owner's own job.
	 */
	request?: string;
	abortController: AbortController;
	promise: Promise<void>;
	resultText?: string;
	errorText?: string;
	/**
	 * Parsed structured completion for a job whose work selected an output
	 * schema. Set from the body's {@link AsyncJobRunResult} or from an
	 * {@link AsyncJobError}. The job row is the carrier — every delivery
	 * attempt, redelivery, and `proc://` snapshot reads it from here.
	 */
	structured?: StructuredSubagentOutput;
	/** Latest progress text the running job reported (a bash job's output tail). */
	progressText?: string;
	/** Latest tool-render details reported by the running job. */
	latestDetails?: AsyncJobDetails;
	/**
	 * Registry id of the agent that registered the job (e.g. "Main",
	 * "AuthLoader"). Used by scoped cancel/list APIs so a subagent's teardown
	 * does not cancel its parent's jobs. Undefined for callers that don't
	 * supply an id (e.g. legacy tests, SDK consumers without an agent context).
	 */
	ownerId?: string;
	/**
	 * Registry id of the subagent this job runs (task/tan/vibe jobs). Lets
	 * job-view code link a job row to its AgentRegistry ref even when the job
	 * id differs from the agent id (vibe turn jobs, tan clones).
	 */
	agentId?: string;
	/**
	 * Whether this job's turn published the agent's `<agentId>.md` output. A
	 * follow-up turn that neither yielded nor produced a message result writes
	 * none, so `agent://<agentId>` still holds an earlier turn's output; job
	 * bodies that know this report it so deliveries advertise only the handles
	 * that answer for this turn. Undefined where the body does not report it.
	 */
	agentOutputPublished?: boolean;
	/** The process the job runs, when it spawns one. */
	process?: AsyncJobProcess;
	/** What the job's live widget reads; set by bash and task jobs. */
	widgetInputs?: JobWidgetInputs;
	/**
	 * Job is registered but parked behind a caller-managed gate (e.g. a task
	 * batch semaphore). Queued jobs do not count toward the running-job limit
	 * until the caller invokes `markRunning()` from the run context.
	 */
	queued?: boolean;
	/**
	 * Job backs a foreground tool call that may still auto-background. It is
	 * hidden from job listings and its delivery is suppressed until
	 * {@link AsyncJobManager.backgroundJob} promotes it; a call that finishes in
	 * the foreground hands it to {@link AsyncJobManager.releaseForegroundJob}.
	 */
	foreground?: boolean;
	/**
	 * Disposal closure for a detached spawn's temporary artifacts directory
	 * that `runStructuredSubagent()` retained past completion (so a
	 * delayed `agent://<id>` handle in an `async-result` delivery still
	 * resolves). The manager invokes and clears this exactly once — on
	 * eviction or manager disposal — so the retained directory does not
	 * outlive the job row it was kept alive for.
	 */
	retainedArtifactsCleanup?: () => Promise<void>;
}
interface AsyncJobProcess {
	/** Full command line; the job label is cut to 120 characters. */
	readonly command: string;
	/** Directory the command started in. */
	readonly cwd: string;
	/** Live pids the command spawned, in spawn order; empty before it starts and after it ends. */
	pids(): readonly number[];
	/** Copies of the bytes crossing the command's pipes, kept with the job row (`proc://<id>/streams`). */
	readonly streams?: PipeStreams;
}
interface SourceMeta$1 {
	/** Provider ID that loaded this item */
	provider: string;
	/** Provider display name (for UI) */
	providerName: string;
	/** Absolute path to the source file */
	path: string;
	/** Whether this came from user-level, project-level, or native config */
	level: "user" | "project" | "native";
	/**
	 * Registry or CLI source that supplied a plugin root, when the provider
	 * tracks it (currently `claude-plugins`: `"claude"` for `~/.claude/plugins`,
	 * `"omp"` for omp's own registry, `"plugin-dir"` for `--plugin-dir`). Lets
	 * user-scope gating distinguish omp's own installs from the foreign Claude
	 * tree — see `isSourceEnabled` in `extensibility/skills.ts` (#10743).
	 */
	origin?: string;
	/**
	 * Plugin or package name supplying this item, for registry-backed providers
	 * (`claude-plugins` and `agent-plugins` use the plugin name, `omp-plugins`
	 * the extension package directory name, `skillshare` the package name).
	 * Preferred by `skillNamespace` in `extensibility/skills.ts` over parsing
	 * the item's path, since installed plugin caches
	 * (`~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/skills/...`)
	 * put the version, not the plugin name, in the path segment owning `skills/`.
	 */
	pluginName?: string;
}
interface Rule {
	/** Rule name (derived from filename) */
	name: string;
	/** Absolute path to rule file */
	path: string;
	/** Rule content (after frontmatter stripped) */
	content: string;
	/** Globs this rule applies to (if any) */
	globs?: string[];
	/** Whether to always include this rule */
	alwaysApply?: boolean;
	/** Description (for agent-requested rules) */
	description?: string;
	/** Regex condition(s) that can trigger TTSR interruption. */
	condition?: string[];
	/** ast-grep pattern condition(s) that can trigger TTSR interruption (edit/write streams only). */
	astCondition?: string[];
	/**
	 * Judged-rule question: asked of the `judge` model role once an in-scope output completes,
	 * never mid-stream. A yes delivers the rule as a non-interrupting warning; `condition` /
	 * `astCondition`, when also set, only gate whether the question is asked.
	 */
	question?: string;
	/** Optional stream scope tokens (for example: text, thinking, tool:edit(*.ts)). */
	scope?: string[];
	/** Lowercased agent-name globs this rule applies to (absent = every agent). */
	agents?: string[];
	/** Per-rule TTSR interrupt mode override (falls back to global ttsr.interruptMode). */
	interruptMode?: "never" | "prose-only" | "tool-only" | "always";
	/** Source metadata */
	_source: SourceMeta$1;
}
declare const COMPACTION_METHOD_CHOICES: readonly [
	{
		readonly value: "remote";
		readonly label: "Server compaction";
		readonly description: "Use provider-native server compaction (OpenAI Responses compact, Anthropic compaction beta, including on Bedrock) when the active route supports it";
	},
	{
		readonly value: "snapcompact";
		readonly label: "Snapcompact";
		readonly description: "Archive history onto dense bitmap images the active vision model reads back; no LLM call";
	},
	{
		readonly value: "handoff";
		readonly label: "Handoff";
		readonly description: "Generate a handoff document and continue from it as the compaction summary";
	},
	{
		readonly value: "soft";
		readonly label: "Soft compaction";
		readonly description: "Summarize in place with a compaction model without using server compaction";
	},
	{
		readonly value: "shake";
		readonly label: "Shake";
		readonly description: "Drop recoverable heavy content in place without an LLM call";
	}
];
type CompactionMethod = (typeof COMPACTION_METHOD_CHOICES)[number]["value"];
interface WorkPoolYieldItem {
	id: string;
	index: number;
}
interface RetryFallbackRole {
	/** Selector the role is assigned (the chain's primary). */
	primary: string;
	/** Fallback selectors walked after the primary; empty explicitly disables fallbacks. */
	chain: string[];
}
interface RuntimeIdentity {
	/** omp package version. */
	version: string;
	/** Source checkout commit; absent for installed builds. */
	commit?: string;
	/** Source checkout branch; absent on a detached HEAD. */
	branch?: string;
	/** Historical uncommitted-diff hash from older session entries; new entries omit it. */
	diff?: string;
	/** Free-form name from `OMP_BUILD_LABEL`. */
	label?: string;
}
declare const TITLE_CHANGE_ENTRY_TYPE = "title_change";
type SessionTitleSource = "auto" | "user";
interface SessionHeader {
	type: "session";
	version?: number;
	id: string;
	title?: string;
	titleSource?: SessionTitleSource;
	timestamp: string;
	cwd: string;
	/**
	 * Additional workspace directories beyond `cwd` (multi-root workspace).
	 * Absolute, normalized, deduplicated. Absent on legacy single-cwd sessions.
	 * See {@link SessionWorkspace} in `./session-workspace`.
	 */
	additionalDirectories?: string[];
	parentSession?: string;
	/**
	 * Set, to the `parentSession` id, when this session is that same conversation
	 * continued in a new file because it could not keep writing its own (another
	 * live process held it, or the file changed under it). Resuming the parent
	 * resumes the most recently active continuation instead. Forks never set it.
	 */
	continuationOf?: string;
	/** Prior absolute JSONL locations recorded by successful session moves. */
	previousSessionFiles?: string[];
	/** Local-only provider prompt-cache identity inherited by exact-route full forks; omitted from shares and HTML. */
	providerPromptCacheKey?: string;
	/**
	 * Session kind. Absent = "agent" (tools enabled). "chat" = tool-free
	 * conversation; stamped at creation, never mutated.
	 */
	kind?: "chat";
	/** Local-only Task candidate, gated by the actual request model's cache policy; omitted from shares and HTML. */
	taskPromptCacheKey?: string;
	/**
	 * Source session file of a read-only seance fork. Its artifacts stay in the
	 * source: `history://`, `artifact://`, and `agent://` read them from there.
	 */
	seanceSource?: string;
}
interface SessionEntryBase {
	type: string;
	id: string;
	parentId: string | null;
	timestamp: string;
	/**
	 * omp build that wrote this entry, set on every entry this session's process authors. Copies
	 * (forks, branches, collab replicas) keep the original writer. Absent on older entries.
	 */
	runtime?: RuntimeIdentity;
	/**
	 * Active leaf at append time, for an entry written off the active branch (late usage,
	 * a background bash result routed back to the branch it started on). The loader replays
	 * it instead of treating this entry as the leaf, so resume keeps the branch the live
	 * session was on. Absence keeps the ordinary last-entry semantics; null records a
	 * cleared leaf.
	 */
	activeLeafId?: string | null;
}
interface SessionMessageEntry extends SessionEntryBase {
	type: "message";
	message: AgentMessage;
}
interface ModelUsageEntry extends SessionEntryBase {
	type: "model_usage";
	purpose: string;
	/** Resolved model role used for the call, such as `tiny` or `smol`. */
	role?: string;
	api: string;
	provider: string;
	model: string;
	usage: Usage;
	stopReason: StopReason;
	errorMessage?: string;
	/**
	 * A cache-warm refresh that answered for the conversation as it stood: when it was sent (a hit restarts
	 * the cache lifetime then) and whether it read the cache. Absent on a failed, replaced or outdated refresh.
	 */
	cacheRefresh?: {
		sentAt: number;
		hit: boolean;
	};
}
interface ThinkingLevelChangeEntry extends SessionEntryBase {
	type: "thinking_level_change";
	thinkingLevel?: string | null;
	/**
	 * The user-configured selector at the time of this change: `"auto"` when auto
	 * mode was active, otherwise the concrete level. Absent on entries written
	 * before auto-mode persistence existed; readers fall back to `thinkingLevel`.
	 */
	configured?: string | null;
}
interface ModelChangeEntry extends SessionEntryBase {
	type: "model_change";
	/** Model in "provider/modelId" format */
	model: string;
	/** Role: "default", "smol", "slow", etc. Undefined treated as "default" */
	role?: string;
	/** True when this transition selected a retry-fallback model rather than the configured model. */
	resolvedModelIsFallback?: boolean;
}
interface ServiceTierChangeEntry extends SessionEntryBase {
	type: "service_tier_change";
	serviceTier: ServiceTierByFamily | null;
}
interface CompactionEntry<T = unknown> extends SessionEntryBase {
	type: "compaction";
	summary: string;
	shortSummary?: string;
	firstKeptEntryId: string;
	tokensBefore: number;
	/** Estimated context tokens after the rewrite (display metadata). */
	tokensAfter?: number;
	/** Method that produced this entry; absent on legacy sessions and extension-provided compactions. */
	method?: CompactionMethod;
	/** Last branch entry represented by provider-native replay history; later entries replay normally. */
	providerReplayThroughEntryId?: string;
	/** Extension-specific data (e.g., ArtifactIndex, version markers for structured compaction) */
	details?: T;
	/** Hook-provided data to persist across compaction */
	preserveData?: Record<string, unknown>;
	/** True if generated by an extension, undefined/false if pi-generated (backward compatible) */
	fromExtension?: boolean;
	/**
	 * Dead-end warning from the post-pass progress guard: the pass completed
	 * but freed too little for maintenance to continue. Rendered on the
	 * compaction divider.
	 */
	warning?: string;
}
interface BranchSummaryEntry<T = unknown> extends SessionEntryBase {
	type: "branch_summary";
	fromId: string;
	summary: string;
	/** Extension-specific data (not sent to LLM) */
	details?: T;
	/** True if generated by an extension, false if pi-generated */
	fromExtension?: boolean;
}
interface ResetBoundaryEntry extends SessionEntryBase {
	type: "reset_boundary";
}
interface CustomEntry<T = unknown> extends SessionEntryBase {
	type: "custom";
	customType: string;
	data?: T;
}
interface LabelEntry extends SessionEntryBase {
	type: "label";
	targetId: string;
	label: string | undefined;
}
interface TitleChangeEntry extends SessionEntryBase {
	type: typeof TITLE_CHANGE_ENTRY_TYPE;
	title: string;
	previousTitle?: string;
	source: SessionTitleSource;
	trigger?: string;
}
interface TtsrInjectionEntry extends SessionEntryBase {
	type: "ttsr_injection";
	/** Names of rules that were injected */
	injectedRules: string[];
}
interface CredentialPinEntry extends SessionEntryBase {
	type: "credential_pin";
	/** Provider id the pin applies to (e.g. "anthropic"). */
	provider: string;
	/** `credentialPinHash()` of the serving account's identity + scope tuple. */
	hash: string;
}
interface SessionInitEntry extends SessionEntryBase {
	type: "session_init";
	/** System prompt blocks exactly as sent to the model; files written before blocks were kept store one joined string. */
	systemPrompt: string[] | string;
	/** Initial task/user message */
	task: string;
	/** Tools available to the agent */
	tools: string[];
	/** Names in `tools` presented as `xd://` devices instead of top-level tools (absent from older files). */
	mountedTools?: string[];
	/** Agent definition name (for example `scout` or `reviewer`). */
	agent?: string;
	/** Semantic model role declared by the agent, retained even after concrete model resolution. */
	modelRole?: string;
	/** Initially resolved provider/model selector for historical display. */
	resolvedModel?: string;
	/** The model came from a per-call `model`; cold revival re-checks it against the current operator grants. */
	perCallModel?: boolean;
	/** Exact canonical provider/id[@upstream] identity, without a thinking suffix; required for per-call revival. */
	perCallModelIdentity?: string;
	/** Configured thinking selector, or null when unset; required for per-call revival. */
	perCallThinkingLevel?: ConfiguredThinkingLevel | null;
	/** Subagent's `subagent:<id>` retry fallback role as installed at spawn; cold revival reinstalls it. Absent when none was installed or on older files. */
	retryFallback?: RetryFallbackRole;
	/** Whether the agent definition is read-only, allowing an exact zero-LoC attribution. */
	readOnly?: boolean;
	/** Output schema if structured output was requested. */
	outputSchema?: unknown;
	/** Enforcement policy recorded with the output schema for faithful revival. */
	outputSchemaMode?: StructuredSubagentSchemaMode;
	/** Whether revival must retain only the explicitly persisted tool names. */
	restrictToolNames?: boolean;
	/** Spawn allowlist the subagent ran with ("" = none, "*" = any, else CSV); absent on pre-spawns files. */
	spawns?: string;
	/** The agent's `readSummarize` setting (`false` = read summarization disabled); absent uses the session default. */
	readSummarize?: boolean;
	/** Effective advisor for this subagent: `"on"` = advisor-role model, else an explicit model pattern; absent = unadvised. */
	advisor?: string;
	/** Effective thresholds for a child with an explicit compaction override. */
	compactionThreshold?: {
		thresholdPercent: number;
		thresholdTokens: number;
	};
	/** True when the subagent ran inside an isolation worktree: never revivable, transcript-only after park. Absent on older files. */
	isolated?: boolean;
	/** Work-pool yield items of the last model call; revival restores them so the yield tool matches. Absent when none. */
	workPoolYieldItems?: WorkPoolYieldItem[];
}
interface ModeChangeEntry extends SessionEntryBase {
	type: "mode_change";
	/** Current mode name, or "none" when exiting a mode */
	mode: string;
	/** Optional mode-specific data (e.g. plan file path) */
	data?: Record<string, unknown>;
}
interface CustomMessageEntry<T = unknown> extends SessionEntryBase {
	type: "custom_message";
	customType: string;
	content: string | (TextContent | ImageContent)[];
	details?: T;
	display: boolean;
	/** Who initiated this message for billing/attribution semantics. */
	attribution?: MessageAttribution;
}
type SessionEntry = SessionMessageEntry | ModelUsageEntry | ThinkingLevelChangeEntry | ModelChangeEntry | ServiceTierChangeEntry | CompactionEntry | BranchSummaryEntry | CustomEntry | CustomMessageEntry | LabelEntry | TitleChangeEntry | TtsrInjectionEntry | SessionInitEntry | ModeChangeEntry | CredentialPinEntry | ResetBoundaryEntry;
type FileEntry = SessionHeader | SessionEntry;
interface SessionTreeNode {
	entry: SessionEntry;
	children: SessionTreeNode[];
	/** Resolved label for this entry, if any */
	label?: string;
}
interface UsageStatistics {
	input: number;
	output: number;
	cacheRead: number;
	cacheWrite: number;
	totalTokens: number;
	orchestrationInput: number;
	orchestrationOutput: number;
	orchestrationCacheRead: number;
	premiumRequests: number;
	cost: number;
	/** Portion of {@link cost} carried by completed `task` results (direct children's spend). */
	subagentCost: number;
}
interface GoalModeState {
	enabled: boolean;
	mode: "active" | "exiting";
	reason?: "completed";
	goal: Goal;
}
interface SubagentProgressPayload {
	index: number;
	agent: string;
	agentSource: AgentSource;
	task: string;
	parentToolCallId?: string;
	/** Registry id of the spawning subagent for nested spawns; absent at the root. */
	parentSubagentId?: string;
	assignment?: string;
	progress: AgentProgress;
	sessionFile?: string;
	/** See {@link SubagentLifecyclePayload.detached}. */
	detached?: boolean;
}
interface SubagentLifecyclePayload {
	id: string;
	agent: string;
	agentSource: AgentSource;
	description?: string;
	status: "started" | "completed" | "failed" | "aborted";
	sessionFile?: string;
	parentToolCallId?: string;
	/** Registry id of the spawning subagent for nested spawns; absent at the root. */
	parentSubagentId?: string;
	index: number;
	/**
	 * Spawn runs as a detached background job: the parent turn keeps working
	 * while this agent runs. Sync task spawns (parent blocked on the call) and
	 * eval `agent()` bridge spawns (rendered inside their eval cell) leave this
	 * unset — surfaces like the subagent HUD only list detached spawns.
	 */
	detached?: boolean;
}
interface SubagentEventPayload {
	id: string;
	event: AgentSessionEvent;
}
type CacheWarmingMode = "off" | "streaming" | "idle";
interface CacheWarmingRefreshStart {
	phase: "streaming" | "idle";
	provider: string;
	model: string;
}
interface CacheWarmingRefreshEnd extends CacheWarmingRefreshStart {
	outcome: "hit" | "miss" | "error" | "aborted";
	/** Present only when onWarmed recorded this refresh, including an aborted one the provider had already accepted. */
	usage?: Usage;
	/** Why warming stopped; absent when warming continues (the refresh rescheduled, or a new request replaced the run). */
	warmingStopReason?: string;
}
interface RetryErrorUpdate {
	entryId: string;
	persistenceKey?: string;
	note: string;
	retryRecovery: AssistantRetryRecovery;
}
type MemoryBackendId = "off" | "local" | "hindsight" | "mnemopi" | "sharpshooter";
interface SessionLeaseReport {
	held: string[];
	unknown: string[];
}
type AsyncJobSnapshotItem = Pick<AsyncJob, "id" | "type" | "status" | "label" | "intent" | "startTime" | "endTime" | "endedAt" | "agentId" | "ownerId"> & {
	cancellationPending?: boolean;
	resultPreview?: string;
	errorPreview?: string;
	previewTruncated?: boolean;
	/** Full command line of a job that runs a process; `label` is cut to 120 characters. */
	command?: string;
	/** The job's live widget (frozen once it settles), when a spec or fallback exists. */
	widget?: JobWidgetView;
	/** One line on what the command does and when it ends, while widgets are on (`JobWidgetRegistry.caption`). */
	caption?: string;
};
interface ContextUsageBreakdown {
	contextWindow: number;
	anchored: boolean;
	usedTokens: number;
	systemPromptTokens: number;
	systemToolsTokens: number;
	systemContextTokens: number;
	instructionFilesTokens: number;
	skillsTokens: number;
	messagesTokens: number;
}
interface SessionStats {
	/** Entire session journal, including pre-compaction entries and sibling branches. */
	history?: UsageStatistics & {
		sampledAt: number;
	};
	sessionFile: string | undefined;
	sessionId: string;
	userMessages: number;
	assistantMessages: number;
	toolCalls: number;
	toolResults: number;
	totalMessages: number;
	tokens: {
		input: number;
		output: number;
		reasoning: number;
		cacheRead: number;
		cacheWrite: number;
		total: number;
	};
	premiumRequests: number;
	cost: number;
	credits?: {
		cost: number;
		committedCost: number;
		acuCost: number;
	};
	/** Concrete provider-routed model ids with finalized turn counts. */
	routedModels?: Record<string, number>;
	/** Failed edits by the engine's `errorKind`. */
	editRejections?: Record<string, number>;
	contextUsage?: ContextUsage;
}
type RestoredQueuedMessage = {
	text: string;
	images?: ImageContent[];
};
interface RestoredQueuedMessageWithDelivery extends RestoredQueuedMessage {
	mode: "steer" | "followUp";
	timestamp: number;
}
interface SessionQueuedMessage extends RestoredQueuedMessage {
	id: string;
	editable: boolean;
	timestamp: number;
}
type AgentSessionEvent = Exclude<AgentEvent, {
	type: "agent_end";
}> | (Extract<AgentEvent, {
	type: "agent_end";
}> & {
	/** False when an async delivery will resume the session before its true final settle. */
	isTerminal?: boolean;
	/**
	 * True when the agent finished its turn: the end is terminal, or the session resumes
	 * only for queued input or background-job results. False while the agent continues its
	 * own work (retry, compaction continuation, stop-time reminders).
	 */
	yielded?: boolean;
	/**
	 * True on a non-terminal end whose only possible resume is a background-job result
	 * (no queued input, no continuation the agent scheduled itself). The wake is not
	 * guaranteed: a cancelled or suppressed job never delivers one.
	 */
	awaitingAsyncWork?: boolean;
}) | {
	type: "auto_compaction_start";
	reason: "threshold" | "overflow" | "idle" | "incomplete";
	action: "context-full" | "remote" | "handoff" | "shake" | "snapcompact";
} | {
	type: "auto_compaction_end";
	action: "context-full" | "remote" | "handoff" | "shake" | "snapcompact";
	result: CompactionResult | undefined;
	aborted: boolean;
	willRetry: boolean;
	errorMessage?: string;
	/** True when compaction was skipped for a benign reason. */
	skipped?: boolean;
} | {
	type: "manual_compaction_start";
	/** The `/compact <mode>` subcommand, when one was given. */
	mode?: string;
} | {
	type: "manual_compaction_end";
	/** Present when the compaction committed. */
	result?: CompactionResult;
	/** True when the compaction was cancelled (abort, or a `session_before_compact` hook). */
	aborted: boolean;
	/** True when there was nothing to compact; `errorMessage` says why. */
	skipped?: boolean;
	errorMessage?: string;
} | {
	type: "auto_retry_start";
	attempt: number;
	maxAttempts: number;
	delayMs: number;
	errorMessage: string;
	errorId?: number;
} | {
	type: "auto_retry_end";
	success: boolean;
	attempt: number;
	finalError?: string;
	retryErrors?: RetryErrorUpdate[];
} | ({
	type: "cache_warming_start";
} & CacheWarmingRefreshStart) | ({
	type: "cache_warming_end";
} & CacheWarmingRefreshEnd) | {
	type: "retry_fallback_applied";
	from: string;
	to: string;
	role: string;
	reason?: string;
} | {
	type: "retry_fallback_succeeded";
	model: string;
	role: string;
} | {
	type: "model_changed";
} | {
	type: "config_warnings_changed";
} | {
	type: "advisor_cost_changed";
} | {
	type: "advisor_yielded";
} | {
	type: "ttsr_triggered";
	rules: Rule[];
} | {
	type: "todo_reminder";
	todos: TodoItem[];
	attempt: number;
	maxAttempts: number;
} | {
	type: "todo_auto_clear";
} | {
	type: "irc_message";
	message: CustomMessage$1;
} | {
	type: "notice";
	level: "info" | "warning" | "error";
	message: string;
	source?: string;
} | {
	type: "thinking_level_changed";
	thinkingLevel: ThinkingLevel | undefined;
	/** The user-configured selector when it differs from the effective level. */
	configured?: ConfiguredThinkingLevel;
	/** The level `auto` resolved to this turn, once classified. */
	resolved?: Effort;
} | {
	type: "goal_updated";
	goal: Goal | null;
	state?: GoalModeState;
}
/**
 * Coalesced authoritative snapshot of the user-restorable queue whenever its
 * displayable contents change. Remote UIs subscribe instead of polling
 * get_queue; advisor cards and internal steers are deliberately absent (same
 * filter as get_queue).
 */
 | {
	type: "queue_update";
	steering: SessionQueuedMessage[];
	followUp: SessionQueuedMessage[];
};
type UsageLimitState = {
	/** Requests are served on the provider's low-priority (slow) lane. */
	stage: "low_priority";
	/** Epoch seconds when the limit that was hit resets. */
	resetsAtSec: number;
	/** Percent of the low-priority allowance still available, when reported. */
	allowanceLeftPercent?: number;
} | {
	/** Requests run on a short wrap-up allowance past the limit. */
	stage: "wrap_up";
	/** Epoch seconds when the limit that was hit resets, if reported. */
	resetsAtSec?: number;
	/** Whether paid extra usage serves requests once the allowance is spent. */
	extraUsage: boolean;
};
type RpcGoalOp = "get" | "create" | "resume" | "pause" | "drop";
interface RpcGoalResult {
	goal: Goal | null;
	state: GoalModeState | null;
}
interface RpcMessagesPage {
	messages: AgentMessage[];
	nextCursor?: string;
	totalMessages: number;
}
declare const debugSchema: FluentType<{
	action: "output" | "sessions" | "modules" | "pause" | "continue" | "threads" | "attach" | "scopes" | "launch" | "evaluate" | "terminate" | "variables" | "disassemble" | "stack_trace" | "read_memory" | "loaded_sources" | "set_breakpoint" | "remove_breakpoint" | "set_instruction_breakpoint" | "remove_instruction_breakpoint" | "data_breakpoint_info" | "set_data_breakpoint" | "remove_data_breakpoint" | "step_over" | "step_in" | "step_out" | "write_memory" | "custom_request";
	offset?: number | undefined;
	cwd?: string | undefined;
	name?: string | undefined;
	data?: string | undefined;
	program?: string | undefined;
	access_type?: "write" | "read" | "readWrite" | undefined;
	levels?: number | undefined;
	timeout?: number | undefined;
	args?: string[] | undefined;
	adapter?: string | undefined;
	file?: string | undefined;
	line?: number | undefined;
	function?: string | undefined;
	condition?: string | undefined;
	hit_condition?: string | undefined;
	expression?: string | undefined;
	context?: string | undefined;
	frame_id?: number | undefined;
	scope_id?: number | undefined;
	variable_ref?: number | undefined;
	pid?: number | undefined;
	port?: number | undefined;
	host?: string | undefined;
	memory_reference?: string | undefined;
	instruction_reference?: string | undefined;
	instruction_count?: number | undefined;
	instruction_offset?: number | undefined;
	count?: number | undefined;
	data_id?: string | undefined;
	command?: string | undefined;
	arguments?: Record<string, unknown> | undefined;
	resolve_symbols?: boolean | undefined;
	allow_partial?: boolean | undefined;
	start_module?: number | undefined;
	module_count?: number | undefined;
}, {
	action: "output" | "sessions" | "modules" | "pause" | "continue" | "threads" | "attach" | "scopes" | "launch" | "evaluate" | "terminate" | "variables" | "disassemble" | "stack_trace" | "read_memory" | "loaded_sources" | "set_breakpoint" | "remove_breakpoint" | "set_instruction_breakpoint" | "remove_instruction_breakpoint" | "data_breakpoint_info" | "set_data_breakpoint" | "remove_data_breakpoint" | "step_over" | "step_in" | "step_out" | "write_memory" | "custom_request";
	offset?: number | undefined;
	cwd?: string | undefined;
	name?: string | undefined;
	data?: string | undefined;
	program?: string | undefined;
	access_type?: "write" | "read" | "readWrite" | undefined;
	levels?: number | undefined;
	timeout?: number | undefined;
	args?: string[] | undefined;
	adapter?: string | undefined;
	file?: string | undefined;
	line?: number | undefined;
	function?: string | undefined;
	condition?: string | undefined;
	hit_condition?: string | undefined;
	expression?: string | undefined;
	context?: string | undefined;
	frame_id?: number | undefined;
	scope_id?: number | undefined;
	variable_ref?: number | undefined;
	pid?: number | undefined;
	port?: number | undefined;
	host?: string | undefined;
	memory_reference?: string | undefined;
	instruction_reference?: string | undefined;
	instruction_count?: number | undefined;
	instruction_offset?: number | undefined;
	count?: number | undefined;
	data_id?: string | undefined;
	command?: string | undefined;
	arguments?: Record<string, unknown> | undefined;
	resolve_symbols?: boolean | undefined;
	allow_partial?: boolean | undefined;
	start_module?: number | undefined;
	module_count?: number | undefined;
}>;
type DebugParams = typeof debugSchema.infer;
interface ExtensionAskDialogOption {
	label: string;
	description?: string;
	preview?: string;
}
interface ExtensionAskDialogQuestion {
	id: string;
	question: string;
	header?: string;
	options: ExtensionAskDialogOption[];
	multi?: boolean;
	recommended?: number;
}
interface ExtensionAskDialogResultItem {
	id: string;
	question: string;
	options: string[];
	multi: boolean;
	selectedOptions: string[];
	customInput?: string;
	customInputImages?: ImageContent[];
	note?: string;
	noteImages?: ImageContent[];
	/** Label of the option the note is on; unset when it is on the custom answer. */
	noteOption?: string;
	timedOut?: boolean;
}
interface ExtensionAskDialogSubmitResult {
	kind: "submit";
	results: ExtensionAskDialogResultItem[];
}
interface ExtensionAskDialogChatResult {
	kind: "chat";
}
type ExtensionAskDialogResult = ExtensionAskDialogSubmitResult | ExtensionAskDialogChatResult;
interface MemoryInjectionPlan {
	notes: string;
	summary: string;
	learned: string;
	/** Approximate tokens each part uses (~4 chars/token, as the budget is counted). */
	tokens: {
		notes: number;
		summary: number;
		learned: number;
		budget: number;
	};
	/** Notes and lessons are injected whole, newest lesson first; these count how many fit. */
	notesIncluded: number;
	lessonsIncluded: number;
	summaryTruncated: boolean;
}
interface MemoryActivity {
	phase: "reading" | "consolidating";
	/** Sessions read so far and expected in this run (reading phase). */
	done: number;
	total: number;
}
interface MemoryListEntry {
	/** The bullet line as stored; edits name the entry by it. */
	line: string;
	text: string;
	/** Lessons only: where the lesson came from. */
	context?: string;
}
type MemoryListEditFailure = "empty" | "not_found" | "full";
type MemoryListEditResult = {
	ok: true;
	line?: string;
} | {
	ok: false;
	reason: MemoryListEditFailure;
};
type MemorySourceState = 
/** Read, and something was worth keeping. */
"remembered"
/** Read; nothing in it was worth keeping. */
 | "nothing"
/** Kept out of memory by the user. */
 | "forgotten"
/** Being read right now. */
 | "reading"
/** Reading failed; it is retried later. */
 | "failed"
/** Changed too recently; read once it has been idle long enough. */
 | "waiting"
/** Eligible and not read yet. */
 | "queued"
/** Older than the pipeline looks back. */
 | "old";
interface MemorySource {
	threadId: string;
	/** The session file; absent when the session was deleted after being read. */
	sessionPath?: string;
	title?: string;
	/** Last change to the session, ms since epoch (0 when unknown). */
	updatedAt: number;
	state: MemorySourceState;
	/** What was taken from it (`remembered` only). */
	summary?: string;
	/** When it was read, ms since epoch. */
	readAt?: number;
	/** It changed after being read; the new part is read on a later update. */
	changedSinceRead?: boolean;
	forgottenAt?: number;
	error?: string;
}
interface MemoryPlaybook {
	name: string;
	description: string;
	content: string;
}
interface MemoryOverview {
	cwd: string;
	/** The project's memory folder (may not exist yet). */
	root: string;
	/** The selected `memory.backend`; the workbench edits only the local one. */
	backend: string;
	enabled: boolean;
	notes: MemoryListEntry[];
	lessons: MemoryListEntry[];
	/** `memory_summary.md`: the consolidated digest every new session starts with. */
	summary: string;
	/** `MEMORY.md`: the full consolidated notes the agent can open on demand. */
	full: string;
	playbooks: MemoryPlaybook[];
	injection: Omit<MemoryInjectionPlan, "notes" | "summary" | "learned">;
	sources: MemorySource[];
	/** Memory work under way for this project, here or in another omp process. */
	activity?: MemoryActivity & {
		elsewhere: boolean;
	};
	consolidation: {
		/** When the summary was last rebuilt, ms since epoch. */
		updatedAt?: number;
		/** The last rebuild failed with this error and will be retried. */
		error?: string;
		/** Sources changed since the last rebuild. */
		pending: boolean;
	};
	limits: {
		idleHours: number;
		maxAgeDays: number;
		perUpdate: number;
	};
	/** Models the next update would use, as `provider/id`. */
	models: {
		read?: string;
		consolidate?: string;
	};
}
interface BtwHistoryTurn {
	question: string;
	/** Images attached to the question, sent again with it on later follow-ups. */
	images?: readonly ImageContent[];
	answer: string;
	status: "running" | "complete" | "cancelled" | "error" | "interrupted";
	createdAt: number;
	updatedAt: number;
	error?: string;
}
interface BtwHistoryRecord extends BtwHistoryTurn {
	id: string;
	leafId: string | null;
	/** Display heading for the topic, e.g. `/sitrep`; `question` keeps the full text sent to the model. */
	title?: string;
	followUps?: readonly BtwHistoryTurn[];
}
type AvailableSlashCommandSource = "builtin" | "skill" | "extension" | "custom" | "mcp_prompt" | "file";
interface RpcBrowserHostInfo {
	/** Websocket URL the host dials as the relay extension (carries its token). */
	extensionUrl: string;
}
/** Structured provenance preserved for GUI settings persistence/source displays. */
export interface RpcSettingProvenance {
	layers: ("global" | "project" | "overlay" | "runtime")[];
	globalValue?: unknown;
	globalPath?: string;
}
/** One chat's own settings (`get_chat_config`, `set_chat_config`); a chat that saved none follows `chat.*`. */
export interface RpcChatConfig {
	/** Sent as the whole system prompt; blank sends the built-in chat prompt. */
	systemPrompt: string;
	/** `single` sends every message on its own. */
	history: "continuous" | "single";
	/** Earlier turns a continuous chat sends with each message; 0 sends all of them. */
	historyTurns: number;
}
/**
 * `set_event_filter` projection: `"full"` keeps both accumulated snapshots in `message_update`, `"delta"` replaces them
 * with the block a start event opens or an end event closes (see {@link RpcDeltaAssistantMessageEvent}).
 */
export type RpcMessageUpdates = "full" | "delta";
export type RpcCommand = {
	id?: string;
	type: "negotiate_protocol";
	protocolVersion: number;
} | {
	id?: string;
	type: "prompt";
	message: string;
	images?: ImageContent[];
	streamingBehavior?: "steer" | "followUp";
} | {
	id?: string;
	type: "steer";
	message: string;
	images?: ImageContent[];
} | {
	id?: string;
	type: "follow_up";
	message: string;
	images?: ImageContent[];
} | {
	id?: string;
	type: "remove_queued_message";
	message: string;
	queue: "steering" | "followUp";
} | {
	id?: string;
	type: "promote_queued_message";
	message: string;
} | {
	id?: string;
	type: "abort";
} | {
	id?: string;
	type: "abort_and_prompt";
	message: string;
	images?: ImageContent[];
} | {
	id?: string;
	type: "abort_and_restore_queue";
} | {
	id?: string;
	type: "new_session";
	parentSession?: string;
} | {
	id?: string;
	type: "drop_session";
} | {
	id?: string;
	type: "open_session";
	sessionDir: string;
	provider?: string;
	modelId?: string;
} | {
	id?: string;
	type: "get_state";
} | {
	id?: string;
	type: "set_fast_mode";
	enabled: boolean;
} | {
	id?: string;
	type: "set_slow_mode";
	enabled: boolean;
} | {
	id?: string;
	type: "goal";
	op: RpcGoalOp;
	objective?: string;
	token_budget?: number;
} | {
	id?: string;
	type: "set_ask_dialog";
	enabled: boolean;
	form?: boolean;
} | {
	id?: string;
	type: "get_available_commands";
} | {
	id?: string;
	type: "get_entries";
	since?: string;
} | {
	id?: string;
	type: "get_tree";
} | {
	id?: string;
	type: "set_todos";
	phases: TodoPhase[];
} | {
	id?: string;
	type: "set_host_tools";
	tools: RpcHostToolDefinition[];
} | {
	id?: string;
	type: "set_host_uri_schemes";
	schemes: RpcHostUriSchemeDefinition[];
} | {
	id?: string;
	type: "set_subagent_subscription";
	level: RpcSubagentSubscriptionLevel;
} | {
	id?: string;
	type: "set_event_filter";
	events: string[] | null;
	messageUpdates?: RpcMessageUpdates;
} | {
	id?: string;
	type: "get_subagents";
} | {
	id?: string;
	type: "get_subagent_messages";
	subagentId?: string;
	sessionFile?: string;
	fromByte?: number;
} | {
	id?: string;
	type: "cancel_subagent";
	subagentId: string;
} | {
	id?: string;
	type: "steer_subagent";
	subagentId: string;
	message: string;
} | {
	id?: string;
	type: "live_start";
	voice?: string;
	instructions?: string;
} | {
	id?: string;
	type: "live_connect";
	sdpOffer: string;
	voice?: string;
	instructions?: string;
} | {
	id?: string;
	type: "live_stop";
} | {
	id?: string;
	type: "live_mute";
	muted?: boolean;
} | {
	id?: string;
	type: "host_dictation_start";
} | {
	id?: string;
	type: "host_dictation_audio";
	pcm16Base64: string;
} | {
	id?: string;
	type: "host_dictation_stop";
} | {
	id?: string;
	type: "host_dictation_cancel";
} | {
	id?: string;
	type: "terminal_open";
	cols?: number;
	rows?: number;
	cwd?: string;
}
/** Run an omp subcommand that brings its own terminal UI in a revealed terminal. */
 | {
	id?: string;
	type: "terminal_run_cli";
	subcommand: RpcTerminalCliSubcommand;
	args?: string[];
}
/** Open a TUI-only screen in a revealed terminal; resolves once it is showing. */
 | {
	id?: string;
	type: "tui_open";
	surface: RpcTuiSurface;
} | {
	id?: string;
	type: "terminal_input";
	terminalId: string;
	data: string;
} | {
	id?: string;
	type: "terminal_resize";
	terminalId: string;
	cols: number;
	rows: number;
} | {
	id?: string;
	type: "terminal_close";
	terminalId: string;
} | {
	id?: string;
	type: "terminal_list";
} | {
	id?: string;
	type: "terminal_snapshot";
	terminalId: string;
}
/**
 * Opt in to running `pty: true` bash calls in a host terminal (`terminal_opened`
 * with `reveal`) and, with `agentTools`, to the agent's `terminal` tool, which
 * reads the host's terminals and runs long-lived commands in new ones.
 */
 | {
	id?: string;
	type: "terminal_configure";
	interactiveCommands: boolean;
	agentTools?: boolean;
}
/** Serve a private browser relay the host dials as its extension; browser opens go to the host's panel. */
 | {
	id?: string;
	type: "browser_host_start";
} | {
	id?: string;
	type: "browser_host_stop";
} | {
	id?: string;
	type: "dictation_start";
	captureId: string;
} | {
	id?: string;
	type: "dictation_stop";
} | {
	id?: string;
	type: "set_model";
	provider: string;
	modelId: string;
} | {
	id?: string;
	type: "cycle_model";
	direction?: "forward" | "backward";
} | {
	id?: string;
	type: "get_available_models";
	forceRefresh?: boolean;
} | {
	id?: string;
	type: "discover_provider_models";
	/** A saved provider whose credential authenticates the request when `apiKey` is absent. */
	provider?: string;
	api: string;
	baseUrl: string;
	apiKey?: string;
	authHeader?: boolean;
	headers?: Record<string, string>;
	/** The server's model-list protocol; `openai-models-list` by default. */
	discoveryType?: "ollama" | "llama.cpp" | "lm-studio" | "openai-models-list" | "proxy" | "litellm";
	timeoutMs?: number;
} | {
	id?: string;
	type: "set_thinking_level";
	level: ThinkingLevel | "auto";
} | {
	id?: string;
	type: "cycle_thinking_level";
} | {
	id?: string;
	type: "get_available_thinking_levels";
} | {
	id?: string;
	type: "set_steering_mode";
	mode: "all" | "one-at-a-time";
} | {
	id?: string;
	type: "set_follow_up_mode";
	mode: "all" | "one-at-a-time";
} | {
	id?: string;
	type: "set_interrupt_mode";
	mode: "immediate" | "wait";
} | {
	id?: string;
	type: "dequeue";
} | {
	id?: string;
	type: "get_queue";
} | {
	id?: string;
	type: "queue_edit";
	queueId: string;
	text: string;
} | {
	id?: string;
	type: "queue_remove";
	queueId: string;
} | {
	id?: string;
	type: "queue_move";
	queueId: string;
	toIndex: number;
	toLane?: "steering" | "followUp";
} | {
	id?: string;
	type: "queue_clear";
	lane?: "steering" | "followUp";
} | {
	id?: string;
	type: "compact";
	customInstructions?: string;
} | {
	id?: string;
	type: "set_auto_compaction";
	enabled: boolean;
} | {
	id?: string;
	type: "set_cache_warming";
	mode: CacheWarmingMode;
} | {
	id?: string;
	type: "set_auto_retry";
	enabled: boolean;
} | {
	id?: string;
	type: "abort_retry";
} | {
	id?: string;
	type: "bash";
	command: string;
	excludeFromContext?: boolean;
} | {
	id?: string;
	type: "abort_bash";
} | {
	id?: string;
	type: "eval";
	language?: RpcEvalLanguage;
	code: string;
	excluded?: boolean;
} | {
	id?: string;
	type: "abort_eval";
} | {
	id?: string;
	type: "get_session_stats";
} | {
	id?: string;
	type: "get_chat_config";
} | {
	id?: string;
	type: "set_chat_config";
	config: Partial<RpcChatConfig>;
} | {
	id?: string;
	type: "set_session_pinned";
	sessionId: string;
	pinned: boolean;
} | {
	id?: string;
	type: "get_session_lease";
	sessionIds: string[];
} | {
	id?: string;
	type: "export_html";
	outputPath?: string;
} | {
	id?: string;
	type: "switch_session";
	sessionPath: string;
	provider?: string;
	modelId?: string;
} | {
	id?: string;
	type: "branch";
	entryId: string;
} | {
	id?: string;
	type: "fork";
	entryId?: string;
} | {
	id?: string;
	type: "navigate_tree";
	entryId: string;
	summarize?: boolean;
	customInstructions?: string;
} | {
	id?: string;
	type: "abort_branch_summary";
} | {
	id?: string;
	type: "set_label";
	entryId: string;
	label?: string;
} | {
	id?: string;
	type: "get_branch_messages";
} | {
	id?: string;
	type: "get_config_warnings";
} | {
	id?: string;
	type: "get_advisor_status";
} | {
	id?: string;
	type: "list_reset_credits";
} | {
	id?: string;
	type: "redeem_reset_credit";
	target: RpcResetCreditTarget;
	label: string;
} | {
	id?: string;
	type: "get_last_assistant_text";
} | {
	id?: string;
	type: "get_copy_targets";
} | {
	id?: string;
	type: "set_session_name";
	name: string;
	sessionPath?: string;
} | {
	id?: string;
	type: "set_entry_label";
	entryId: string;
	label?: string;
} | {
	id?: string;
	type: "handoff";
	customInstructions?: string;
} | {
	id?: string;
	type: "get_messages";
} | {
	id?: string;
	type: "get_messages_page";
	cursor?: string;
	limit?: number;
} | {
	id?: string;
	type: "get_transcript_page";
	cursor?: string;
	limit?: number;
} | {
	id?: string;
	type: "get_login_providers";
} | {
	id?: string;
	type: "login";
	providerId: string;
} | {
	id?: string;
	type: "set_api_key";
	providerId: string;
	apiKey: string;
} | {
	id?: string;
	type: "get_logout_accounts";
	providerId: string;
} | {
	id?: string;
	type: "logout";
	providerId: string;
	credentialId: number;
} | {
	id?: string;
	type: "get_usage";
} | {
	id?: string;
	type: "get_settings_schema";
} | {
	id?: string;
	type: "get_settings";
	paths?: string[];
} | {
	id?: string;
	type: "set_setting";
	path: string;
	value: unknown;
} | {
	id?: string;
	type: "get_providers";
	forceRefresh?: boolean;
} | {
	id?: string;
	type: "set_plan_mode";
	enabled: boolean;
} | {
	id?: string;
	type: "get_plan_mode";
} | {
	id?: string;
	type: "plan_approval";
	approved: boolean;
	option?: RpcPlanApprovalOption;
	feedback?: string;
	savePath?: string;
} | {
	id?: string;
	type: "get_vibe_mode";
} | {
	id?: string;
	type: "set_vibe_mode";
	enabled: boolean;
} | {
	id?: string;
	type: "get_goal";
} | {
	id?: string;
	type: "guided_goal";
	initial?: string;
} | {
	id?: string;
	type: "set_agents_paused";
	enabled: boolean;
} | {
	id?: string;
	type: "btw_branch";
} | {
	id?: string;
	type: "tan";
	work: string;
} | {
	id?: string;
	type: "omfg";
	complaint: string;
} | {
	id?: string;
	type: "set_goal";
	objective?: string;
	/** Token budget for the goal; `null` clears it (the TUI's `off`). */
	tokenBudget?: number | null;
	/** Lifecycle action on an existing goal; omit to set/replace by objective. */
	action?: "pause" | "resume" | "drop";
} | {
	id?: string;
	type: "get_loop_mode";
} | {
	id?: string;
	type: "set_loop_mode";
	enabled: boolean;
	/** Raw `/loop` argument string (`"10"`, `"5m keep going"`, …) parsed by the TUI's own parser. */
	args?: string;
} | {
	id?: string;
	type: "get_model_roles";
} | {
	id?: string;
	type: "set_model_role";
	role: string;
	modelId: string | null;
	/** Stored as `modelId`'s `:level` suffix; must be one of the candidate's `thinkingLevels`. */
	thinkingLevel?: ConfiguredThinkingLevel;
} | {
	id?: string;
	type: "get_model_role_metadata";
} | {
	id?: string;
	type: "get_skills";
} | {
	id?: string;
	type: "get_skill_detail";
	name: string;
} | {
	id?: string;
	type: "get_hooks";
} | {
	id?: string;
	type: "get_mcp_servers";
} | {
	id?: string;
	type: "get_gui_themes";
} | {
	id?: string;
	type: "get_plugins";
} | {
	id?: string;
	type: "get_marketplaces";
} | {
	id?: string;
	type: "get_prompt_templates";
} | {
	id?: string;
	type: "get_memory_report";
} | {
	id?: string;
	type: "get_memory_overview";
	cwd?: string;
} | {
	id?: string;
	type: "get_memory_activity";
	cwd?: string;
} | {
	id?: string;
	type: "edit_memory";
	cwd?: string;
	edit: RpcMemoryEdit;
} | {
	id?: string;
	type: "update_memory";
	cwd?: string;
	minIdleMinutes?: number;
} | {
	id?: string;
	type: "get_security_dashboard";
} | {
	id?: string;
	type: "get_security_scan";
	scanId: string;
} | {
	id?: string;
	type: "get_ssh_hosts";
} | {
	id?: string;
	type: "get_omp_update";
} | {
	id?: string;
	type: "get_context_report";
} | {
	id?: string;
	type: "get_active_tools";
} | {
	id?: string;
	type: "preview_share_session";
} | {
	id?: string;
	type: "share_session";
	snapshotId?: string;
} | {
	id?: string;
	type: "get_jobs";
} | {
	id?: string;
	type: "set_prewalk";
	enabled: boolean;
} | {
	id?: string;
	type: "fresh";
} | {
	id?: string;
	type: "shake_context";
	mode: "elide" | "images" | "thinking";
} | {
	id?: string;
	type: "reload_plugins";
} | {
	id?: string;
	type: "set_force_tool";
	tool?: string;
	clear?: boolean;
} | {
	id?: string;
	type: "get_force_tool";
} | {
	id?: string;
	type: "set_skill_enabled";
	name: string;
	enabled: boolean;
} | {
	id?: string;
	type: "manage_skill";
	action: "create" | "update" | "delete";
	name: string;
	description?: string;
	body?: string;
} | {
	id?: string;
	type: "set_hook_enabled";
	hookId: string;
	enabled: boolean;
} | {
	id?: string;
	type: "set_plugin_enabled";
	pluginId: string;
	enabled: boolean;
	scope?: "user" | "project";
} | {
	id?: string;
	type: "mcp_action";
	name: string;
	action: "enable" | "disable" | "reconnect" | "remove";
	/** Config file for `remove` (defaults to project, mirroring `/mcp remove`). */
	scope?: "user" | "project";
} | {
	id?: string;
	type: "security_start";
	target: RpcSecurityTargetInput;
} | {
	id?: string;
	type: "security_cancel";
	operationId: string;
} | {
	id?: string;
	type: "security_validate";
	scanId: string;
	findingId: string;
} | {
	id?: string;
	type: "security_set_disposition";
	scanId: string;
	findingId: string;
	status: RpcSecurityDispositionStatus;
	rationale?: string;
} | {
	id?: string;
	type: "ssh_manage";
	action: "create" | "update" | "delete";
	scope: "user" | "project";
	name: string;
	previousName?: string;
	previousScope?: "user" | "project";
	host?: RpcSshHostInput;
} | {
	id?: string;
	type: "ssh_test";
	host: RpcSshHostInput & {
		name: string;
	};
} | {
	id?: string;
	type: "get_session_tree";
} | {
	id?: string;
	type: "get_themes";
} | {
	id?: string;
	type: "get_theme_colors";
	name: string;
} | {
	id?: string;
	type: "get_transcript";
} | {
	id?: string;
	type: "transcribe_audio";
	audioBase64: string;
	mimeType: string;
} | {
	id?: string;
	type: "synthesize_speech";
	text: string;
} | {
	id?: string;
	type: "retry";
} | {
	id?: string;
	type: "clear_context";
} | {
	id?: string;
	type: "abort_subagent";
	agentId: string;
} | {
	id?: string;
	type: "revive_subagent";
	agentId: string;
} | {
	id?: string;
	type: "get_agent_definitions";
} | {
	id?: string;
	type: "write_local_paste";
	content: string;
} | {
	id?: string;
	type: "list_foreign_sessions";
	source: "claude" | "codex";
} | {
	id?: string;
	type: "import_foreign_session";
	source: "claude" | "codex";
	foreignId: string;
} | {
	id?: string;
	type: "fork_from";
	entryId: string;
} | {
	id?: string;
	type: "switch_leaf";
	entryId: string;
	summarize?: boolean;
	customInstructions?: string;
} | {
	id?: string;
	type: "resume_after_ask_reanswer";
} | {
	id?: string;
	type: "get_command_arg_completions";
	command: string;
	prefix: string;
} | {
	id?: string;
	type: "mcp_add";
	name: string;
	config: RpcMcpServerInput;
	scope?: "user" | "project";
} | {
	id?: string;
	type: "mcp_test";
	name?: string;
	config?: RpcMcpServerInput;
} | {
	id?: string;
	type: "mcp_reauth";
	name: string;
} | {
	id?: string;
	type: "mcp_reauth_cancel";
	name: string;
} | {
	id?: string;
	type: "marketplace_action";
	action: "add" | "remove" | "update" | "install" | "uninstall" | "upgrade" | "list_available";
	marketplace?: string;
	plugin?: string;
	source?: string;
} | {
	id?: string;
	type: "debug";
	params: DebugParams;
} | {
	id?: string;
	type: "collab_start";
	relayUrl?: string;
	view?: boolean;
} | {
	id?: string;
	type: "collab_join";
	link: string;
} | {
	id?: string;
	type: "collab_leave";
} | {
	id?: string;
	type: "get_collab_state";
} | {
	id?: string;
	type: "get_plugin_detail";
	pluginId: string;
} | {
	id?: string;
	type: "set_plugin_features";
	pluginId: string;
	features: string[];
} | {
	id?: string;
	type: "set_plugin_setting";
	pluginId: string;
	key: string;
	value: unknown;
} | {
	id?: string;
	type: "delete_plugin_setting";
	pluginId: string;
	key: string;
} | {
	id?: string;
	type: "get_directories";
} | {
	id?: string;
	type: "add_directory";
	path: string;
} | {
	id?: string;
	type: "remove_directory";
	path: string;
} | {
	id?: string;
	type: "move_session";
	path: string;
} | {
	id?: string;
	type: "get_git_status";
} | {
	id?: string;
	type: "get_git_changes";
} | {
	id?: string;
	type: "get_git_diff";
	path: string;
} | {
	id?: string;
	type: "worktree_create";
	name: string;
	baseCwd?: string;
	baseRef?: "HEAD" | "default";
} | {
	id?: string;
	type: "worktree_remove";
	path: string;
	force?: boolean;
} | {
	id?: string;
	type: "pr_repo";
} | {
	id?: string;
	type: "pr_list";
	state?: "open" | "closed" | "merged" | "all";
	limit?: number;
} | {
	id?: string;
	type: "pr_get";
	number: number;
} | {
	id?: string;
	type: "pr_diff";
	number: number;
	path: string;
} | {
	id?: string;
	type: "pr_draft";
	base?: string;
	head?: string;
} | {
	id?: string;
	type: "pr_create";
	title: string;
	body: string;
	base?: string;
	head?: string;
	draft?: boolean;
} | {
	id?: string;
	type: "pr_checkout";
	number: number;
} | {
	id?: string;
	type: "predict_word";
	text: string;
	cursor: number;
} | {
	id?: string;
	type: "predict_word_feedback";
	text: string;
	cursor: number;
	suggestion: string;
	accepted: boolean;
} | {
	id?: string;
	type: "btw";
	question: string;
	recordId?: string;
} | {
	id?: string;
	type: "btw_cancel";
	recordId?: string;
} | {
	id?: string;
	type: "btw_inject";
	recordId: string;
} | {
	id?: string;
	type: "get_btw_history";
};
export interface RpcSessionState {
	collab?: RpcCollabState;
	model?: Model;
	thinkingLevel: ThinkingLevel | undefined;
	/** Configured selector: `auto` while auto mode is active, else the effective level. Drives pickers (Codex-style) rather than cyclers. */
	thinkingConfigured?: ConfiguredThinkingLevel;
	/** Levels the active model actually supports (empty = model does not reason); pickers must not offer unsupported values. */
	availableThinkingLevels?: ThinkingLevel[];
	isStreaming: boolean;
	isCompacting: boolean;
	steeringMode: "all" | "one-at-a-time";
	followUpMode: "all" | "one-at-a-time";
	interruptMode: "immediate" | "wait";
	sessionFile?: string;
	cwd: string;
	sessionId: string;
	sessionName?: string;
	autoCompactionEnabled: boolean;
	autoRetryEnabled: boolean;
	fastModeEnabled: boolean;
	fastModeActive: boolean;
	/** `/slow` applies to the active model (flex tier on OpenAI/Google, low priority on Claude subscriptions). */
	slowModeSupported: boolean;
	/** `/slow` is on for the active model; always `false` when `slowModeSupported` is `false`. */
	slowModeEnabled: boolean;
	/**
	 * Where the active model's `/slow` lives: `global` (persisted config shared by every
	 * session, e.g. Claude low priority) or `session` (this session's flex tier). Absent when unsupported.
	 */
	slowModeScope?: "session" | "global";
	/** Usage-limit stage of the active model's account; absent outside wrap-up and low priority. */
	usageLimit?: UsageLimitState;
	tokensPerSecond: number | null;
	messageCount: number;
	queuedMessageCount: number;
	/** Background jobs or deliveries can still inject a follow-up and wake the session. */
	hasPendingAsyncWork: boolean;
	/** Same predicate as `session_settled`: idle with nothing queued or pending. */
	isSettled: boolean;
	/** Displayable queue-chip text for pending user-authored messages, mirroring
	 *  `AgentSession.getQueuedMessages()`. Render the queue from this snapshot
	 *  (and the `queue_update` event) instead of tracking chips independently.
	 *  `liveSteered` counts the leading `steering` entries already sent into the
	 *  streaming response; `remove_queued_message` cannot reach them. */
	queuedMessages: {
		steering: string[];
		followUp: string[];
		liveSteered: number;
	};
	todoPhases: TodoPhase[];
	/** For session dump / export (plain-text parity with /dump). */
	systemPrompt?: string[];
	dumpTools?: Array<{
		name: string;
		description: string;
		parameters: unknown;
		examples?: readonly ToolExample[];
	}>;
	/** Current context window usage. */
	contextUsage?: ContextUsage;
	/** Whether plan mode is currently enabled. */
	planModeEnabled: boolean;
	/** Whether a prewalk model switch is armed and waiting for the first edit/write. */
	prewalkArmed?: boolean;
	/** Process-global agent pause state. */
	agentsPaused: boolean;
	agentsPausedAt?: number;
	/** Session kind. Absent = "agent" (tools enabled); "chat" = tool-free conversation. Immutable per session. */
	kind?: "chat";
	/** Current goal-mode state; `null` when the session has no goal. */
	goal: GoalModeState | null;
}
/** Authoritative selector/effective pair returned after set_thinking_level. */
export interface RpcThinkingLevelState {
	thinkingLevel: ThinkingLevel | undefined;
	thinkingConfigured: ConfiguredThinkingLevel | undefined;
}
export interface RpcAvailableSlashCommand {
	name: string;
	aliases?: string[];
	description?: string;
	input?: {
		hint?: string;
	};
	subcommands?: Array<{
		name: string;
		description?: string;
		usage?: string;
	}>;
	source: AvailableSlashCommandSource;
	/** Whether the command consumes text after its name (drives post-space completion). */
	allowArgs?: boolean;
	/** Whether dynamic candidates exist (get_command_arg_completions); static-only commands never need the round trip. */
	hasDynamicArgCompletion?: boolean;
}
/** Result of switch_leaf (session.navigateTree passthrough + the leaf after the move). */
export interface RpcSwitchLeafResult {
	cancelled: boolean;
	aborted?: boolean;
	/** Set when the target is an `ask` toolResult and the caller must re-open the ask picker first (issue #5642). */
	reopenAsk?: {
		toolCallId: string;
		html: string;
	};
	/** Draft text/images of the target user message — restore into the composer. */
	editorText?: string;
	editorImages?: ImageContent[];
	/** The active leaf after navigation (undefined when cancelled/aborted). */
	activeLeafId?: string;
	/** The caller must rebuild its transcript, then send resume_after_ask_reanswer. */
	askReanswerCommitted?: boolean;
}
export interface RpcCollabParticipant {
	name: string;
	role: "host" | "guest";
	readOnly?: boolean;
}
/** Current live-collaboration attachment. Secret write links never enter logs. */
export interface RpcCollabState {
	role: "host" | "guest" | null;
	readOnly: boolean;
	link?: string;
	viewLink?: string;
	webLink?: string;
	webViewLink?: string;
	participants: RpcCollabParticipant[];
}
/** Lightweight metadata of one Claude/Codex session offered for import. */
export interface RpcForeignSessionInfo {
	id: string;
	path: string;
	cwd: string;
	title?: string;
	created: string;
	modified: string;
	messageCount?: number;
	firstMessage?: string;
}
export interface RpcAvailableCommandsUpdateFrame {
	type: "available_commands_update";
	commands: RpcAvailableSlashCommand[];
}
/** How a prompt's work ended, as reported by its {@link RpcPromptResultFrame}. */
export type RpcPromptStatus = "completed" | "aborted" | "error";
/**
 * Failure detail for a `prompt_result` with `status: "error"`. `message` is the
 * provider's error text without OMP-local diagnostics (e.g. request dump paths).
 */
export interface RpcPromptError {
	message: string;
	provider?: string;
	model?: string;
	/** HTTP status reported by the provider, when the failure came from a request. */
	httpStatus?: number;
	/** The failure is classified transient: resubmitting later may succeed. OMP's own retries are already exhausted. */
	retryable: boolean;
	/** `cost_cap`: the session reached `session.maxCost` (`--max-cost`) and omp did not send the next request. */
	code?: "cost_cap";
}
/**
 * Terminal frame emitted exactly once per accepted `prompt`/`abort_and_prompt`,
 * after all work the prompt caused has settled. Correlate on `id`.
 */
export interface RpcPromptResultFrame {
	type: "prompt_result";
	id?: string;
	/** False when the prompt completed locally (slash command) or failed before reaching the agent. */
	agentInvoked: boolean;
	status: RpcPromptStatus;
	error?: RpcPromptError;
	/**
	 * The agent yielded and nothing will wake the session again: no run is live and no
	 * queued message or background job (async bash/task/eval) will inject a follow-up.
	 * When false, a {@link RpcSessionSettledFrame} follows once that work is done.
	 */
	sessionSettled: boolean;
}
/**
 * Emitted when the session goes quiet after agent activity: the last run yielded
 * and no background work remains that could inject messages and wake it again.
 * Distinct from a terminal `agent_end`, which only means one run yielded.
 */
export interface RpcSessionSettledFrame {
	type: "session_settled";
}
/** Live session phase change. */
export interface RpcLivePhaseFrame {
	type: "live_phase";
	phase: LivePhase;
}
/** Microphone/speaker RMS in [0, 1], at most one frame per 100 ms carrying the latest values. */
export interface RpcLiveLevelsFrame {
	type: "live_levels";
	input: number;
	output: number;
}
/** Incremental (`final: false`) or final transcript of one realtime turn; coalesce on `role` + `turn`. */
export interface RpcLiveTranscriptFrame {
	type: "live_transcript";
	role: "user" | "assistant";
	turn: number;
	text: string;
	final: boolean;
}
/** Emitted exactly once per live session when it has ended; `error` carries the failure cause. */
export interface RpcLiveEndFrame {
	type: "live_end";
	error?: string;
}
export type RpcLiveFrame = RpcLivePhaseFrame | RpcLiveLevelsFrame | RpcLiveTranscriptFrame | RpcLiveEndFrame;
/**
 * `preparing`: loading (or downloading) the model, microphone audio is not yet consumed;
 * `recording`: `host_dictation_audio` is transcribed live; `transcribing`: finishing after stop.
 */
export type RpcHostDictationState = "preparing" | "recording" | "transcribing" | "idle";
export interface RpcHostDictationStateFrame {
	type: "host_dictation_state";
	state: RpcHostDictationState;
}
/**
 * Edits to the host's text box. `volatile` replaces the in-progress words (not yet final);
 * `commit` inserts final text at the dictation point and clears the volatile words; `clear` drops
 * them; `trim` deletes `count` characters before the dictation point (a spoken submit command);
 * `submit` sends the message, per the `stt.submitTrigger` setting.
 */
export type RpcHostDictationTextFrame = {
	type: "host_dictation_text";
	op: "volatile";
	text: string;
} | {
	type: "host_dictation_text";
	op: "commit";
	text: string;
} | {
	type: "host_dictation_text";
	op: "clear";
} | {
	type: "host_dictation_text";
	op: "trim";
	count: number;
} | {
	type: "host_dictation_text";
	op: "submit";
};
/** A warning (the capture failed or cannot start) or a status line (e.g. "No speech detected."). */
export interface RpcHostDictationNoticeFrame {
	type: "host_dictation_notice";
	level: "warning" | "status";
	message: string;
}
/** Download progress of the dictation model while preparing, as an integer percent. */
export interface RpcHostDictationDownloadFrame {
	type: "host_dictation_download";
	label: string;
	percent: number;
}
export type RpcHostDictationFrame = RpcHostDictationStateFrame | RpcHostDictationTextFrame | RpcHostDictationNoticeFrame | RpcHostDictationDownloadFrame;
/** One host-rendered terminal. */
/** `get_advisor_status`: the roster behind the TUI's advisor eye badge, plus advisor spend. */
export interface RpcAdvisorStatus {
	/** `advisor.enabled` is on; `advisors` lists the configured roster either way. */
	configured: boolean;
	advisors: Array<{
		name: string;
		status: "running" | "paused" | "quota_exhausted" | "error" | "no_model";
		/** Every comment on the yielded primary turn is in; none arrive until the next turn. */
		yielded: boolean;
	}>;
	/** Cumulative advisor cost recorded for this session, in USD. */
	cost: number;
	usingSubscription: boolean;
}
/** A saved-reset option bound to one stored credential; pass back unchanged to `redeem_reset_credit`. */
export interface RpcResetCreditTarget {
	provider: string;
	credentialId: number;
	creditId?: string;
	accountId?: string;
	email?: string;
	orgId?: string;
}
/** One provider account row of `list_reset_credits`, active account first. */
export interface RpcResetCreditAccount {
	label: string;
	provider: string;
	providerLabel: string;
	/** Banked resets, including grants that cannot be spent right now. */
	availableCount: number;
	/** Resets the provider currently permits this account to spend. */
	redeemableCount: number;
	target: RpcResetCreditTarget;
	active: boolean;
	error?: string;
	unavailableReason?: string;
	expiresAt?: string;
}
/** `redeem_reset_credit`: whether a reset was applied, plus the TUI's human-facing summary. */
export interface RpcResetCreditRedeemResult {
	ok: boolean;
	code: string;
	message: string;
}
/** omp subcommands `terminal_run_cli` may launch: each renders its own full-screen terminal UI. */
export type RpcTerminalCliSubcommand = "git" | "cleanse";
/** TUI-only screens `tui_open` hosts in a terminal view. */
export type RpcTuiSurface = "advisor_config";
export interface RpcTerminalInfo {
	terminalId: string;
	/**
	 * `shell`: a login shell the host opened; `command`: a `pty: true` bash tool
	 * call the agent waits on; `background`: a long-lived command the agent
	 * started with the `terminal` tool (a dev server, a watcher); `session`: a
	 * shell the agent drives command by command with the `terminal` tool, on
	 * this machine or over SSH.
	 */
	kind: "shell" | "command" | "background" | "session";
	/** Shell name, the command's first line, or a session's label. */
	title: string;
	cwd: string;
	running: boolean;
	/** Exit code once the process ended (absent when it was killed by a signal). */
	exitCode?: number;
	pid?: number;
	cols: number;
	rows: number;
	/** SSH destination of a remote session. */
	host?: string;
	/**
	 * A session's state: `starting` until its shell is ready, `idle` at the
	 * prompt, `busy` running the agent's command, `input` waiting for the user
	 * to answer a password, passphrase, or host-key question.
	 */
	state?: "starting" | "idle" | "busy" | "input";
	/** The session's shell runs as root. */
	root?: boolean;
}
/** A terminal exists; `reveal` asks the host to show it (an agent command waits for the user). */
export interface RpcTerminalOpenedFrame {
	type: "terminal_opened";
	terminal: RpcTerminalInfo;
	reveal?: boolean;
}
/** Raw PTY output; `seq` counts characters sent for this terminal through the end of `data`. */
export interface RpcTerminalOutputFrame {
	type: "terminal_output";
	terminalId: string;
	data: string;
	seq: number;
}
/** The terminal's process ended; the terminal stays until closed. */
export interface RpcTerminalExitFrame {
	type: "terminal_exit";
	terminalId: string;
	exitCode?: number;
	cancelled: boolean;
	timedOut: boolean;
}
/** The terminal is gone. */
export interface RpcTerminalClosedFrame {
	type: "terminal_closed";
	terminalId: string;
}
export type RpcTerminalFrame = RpcTerminalOpenedFrame | RpcTerminalOutputFrame | RpcTerminalExitFrame | RpcTerminalClosedFrame;
/** `terminal_snapshot` result: recent output through `seq`; later frames have a larger `seq`. */
export interface RpcTerminalSnapshot {
	terminal: RpcTerminalInfo;
	data: string;
	seq: number;
}
/** Draft-only dictation frames bypass the session event filter. */
export interface RpcDictationStateFrame {
	type: "dictation_state";
	captureId: string;
	state: "starting" | "recording" | "transcribing";
}
/** Full accumulated preview, replacing the previous preview for this capture. */
export interface RpcDictationTranscriptFrame {
	type: "dictation_transcript";
	captureId: string;
	text: string;
}
/** Exactly one terminal frame per capture, including any failure. */
export interface RpcDictationEndFrame {
	type: "dictation_end";
	captureId: string;
	text: string;
	error?: string;
}
export type RpcDictationFrame = RpcDictationStateFrame | RpcDictationTranscriptFrame | RpcDictationEndFrame;
/** `open_session` result: `resumed` is false when a fresh session was started in the directory. */
export interface RpcOpenSessionResult {
	cancelled: boolean;
	resumed: boolean;
	sessionId: string;
	sessionFile?: string;
}
/** `navigate_tree` result. */
export interface RpcNavigateTreeResult {
	cancelled: boolean;
	/** Only ever `true`: `abort_branch_summary` stopped the summary; nothing moved. */
	aborted?: boolean;
	/** The target was a user message: its text, for the editor (the leaf moved to its parent). */
	editorText?: string;
}
/** `remove_queued_message` result. */
export interface RpcRemoveQueuedMessageResult {
	removed: boolean;
	/** The removed message's images, so the client can restore them with its text. */
	images?: ImageContent[];
	/** Set when the images exceeded the transport limit and were omitted; the removal still happened. */
	imagesDropped?: true;
}
/** `abort_and_restore_queue` result: the user-authored queued input withdrawn before the abort, oldest first. */
export interface RpcAbortAndRestoreQueueResult {
	steering: RestoredQueuedMessage[];
	followUp: RestoredQueuedMessage[];
	/** Set when the full result exceeded the transport limit and every entry's `images` was omitted. */
	imagesDropped?: true;
	/** Set when even the text-only result exceeded the limit: only an oldest-first prefix is listed. */
	truncated?: true;
}
export interface RpcReadyFrame {
	type: "ready";
	protocolVersion: 1;
	supportedProtocolVersions: [
		1,
		2
	];
	maxFrameBytes: number;
	maxReassembledFrameBytes: number;
	/** `bash` accepts `excludeFromContext`; older runtimes omit this capability. Require exactly `true`. */
	supportsBashExcludeFromContext?: boolean;
}
export interface RpcChunkFrame {
	type: "rpc_chunk";
	chunkId: string;
	index: number;
	count: number;
	byteLength: number;
	data: string;
}
export interface RpcHandoffResult {
	savedPath?: string;
}
/**
 * Languages accepted by the `eval` command. Only `"python"` is wired to the
 * interactive user-eval path today (`AgentSession.executePython`, the same
 * machinery the TUI `$`/`$$` composer modes use); the other eval backends
 * remain agent-tool-only and the command rejects them with an error response.
 */
export type RpcEvalLanguage = "python" | "js" | "ruby" | "julia";
/**
 * Result of an `eval` command: the execution outcome plus a request echo so
 * the host can render an execution bubble without retaining the request.
 * Operational failures (kernel unavailable, disposal in progress) arrive as
 * `success: false` error responses instead — a non-zero `exitCode` here is a
 * user-code failure, not a transport/handler failure.
 */
export interface RpcEvalResult {
	language: RpcEvalLanguage;
	code: string;
	/** Combined stdout + stderr (sanitized, possibly truncated). */
	output: string;
	/** Kernel exit code (0 ok, 1 error); omitted when the run was cancelled. */
	exitCode?: number;
	/** Whether the execution was cancelled (abort_eval / session dispose). */
	cancelled: boolean;
	/** Whether the output was truncated. */
	truncated: boolean;
	/** Echo of the request's `excluded` flag (the `$$` exclude-from-context form). */
	excluded: boolean;
}
/**
 * Result of a `dequeue` command. Messages retain their original delivery lane
 * and are ordered by enqueue time across both queues (oldest first).
 */
export interface RpcDequeueResult {
	messages: RestoredQueuedMessageWithDelivery[];
}
/**
 * One user-restorable queued message surfaced by `get_queue`. `id` is the
 * stable per-entry queue id assigned at enqueue time (lane-prefixed counter
 * like `s1`/`f1`), valid for queue operations until the entry is consumed or
 * removed. Only user-authored, displayable entries appear here; `editable`
 * distinguishes plain user prompts from structured command payloads.
 */
export interface RpcQueuedMessage {
	id: string;
	text: string;
	images?: ImageContent[];
	editable: boolean;
	timestamp: number;
}
/** Result of a `get_queue` command: both lanes in visible execution order. */
export interface RpcGetQueueResult {
	steering: RpcQueuedMessage[];
	followUp: RpcQueuedMessage[];
}
/** Result of a `queue_edit` command. Unknown/read-only ids produce an error response. */
export interface RpcQueueEditResult {
	updated: true;
}
/** Result of a `queue_remove` command. Unknown ids produce an error response instead. */
export interface RpcQueueRemoveResult {
	removed: true;
}
/** Result of a `queue_move` command: the entry's lane and final visible index. */
export interface RpcQueueMoveResult {
	lane: "steering" | "followUp";
	index: number;
}
/** Result of a `queue_clear` command: count of user-restorable messages removed. */
export interface RpcQueueClearResult {
	removed: number;
}
/** A single normalized usage limit bucket for one provider window/quota. */
export interface RpcUsageLimit {
	id: string;
	label: string;
	/** Used fraction 0..1 (>1 = overage); undefined when the provider omits it. */
	usedFraction?: number;
	used?: number;
	limit?: number;
	unit?: string;
	remainingFraction?: number;
	windowLabel?: string;
	resetsAt?: number;
	status?: string;
	notes?: string[];
}
/** Aggregated usage report for one provider/account. */
export interface RpcUsageReport {
	provider: string;
	fetchedAt: number;
	limits: RpcUsageLimit[];
	notes?: string[];
	/** Account identity (email/org) when the provider reports it. */
	account?: string;
	/** Saved rate-limit resets available to redeem, when reported. */
	resetCreditsAvailable?: number;
}
/** Local session token/cost tallies (fallback when no provider usage API). */
export interface RpcUsageSessionStats {
	input: number;
	output: number;
	cacheRead: number;
	cacheWrite: number;
	totalTokens: number;
	orchestrationTokens: number;
	premiumRequests: number;
	cost: number;
}
export interface RpcUsageResult {
	/** Provider-reported quota reports; empty when the provider has no usage API. */
	reports: RpcUsageReport[];
	session: RpcUsageSessionStats;
}
/** One setting entry from the unified schema, projected for the GUI. */
export interface RpcSettingEntry {
	provenance?: RpcSettingProvenance;
	path: string;
	type: "boolean" | "string" | "number" | "enum" | "array" | "record";
	value: unknown;
	default: unknown;
	label?: string;
	description?: string;
	tab?: string;
	group?: string;
	/** Enum choices (for `enum` type) or submenu options. */
	options?: Array<{
		value: string;
		label: string;
		description?: string;
	}>;
	/** True when the value is a credential and must be masked in the UI. */
	secret?: boolean;
	/** True when the setting has no UI metadata (config-file only). */
	advanced?: boolean;
	/**
	 * Visibility gate name from the schema ui metadata (e.g. "advisorEnabled").
	 * Clients that can resolve the gate hide the entry while it evaluates false;
	 * unknown gates stay visible.
	 */
	condition?: string;
	/** True when array order is meaningful and editors should support reordering. */
	ordered?: boolean;
	/**
	 * True when the setting only affects TUI chrome. Non-TUI clients should
	 * hide it or clearly identify that it has no effect there.
	 */
	tuiOnly?: boolean;
	/**
	 * True when the value is cached at session/agent construction: edits take
	 * effect only after a sidecar/session restart, in every client.
	 */
	restartRequired?: boolean;
}
export interface RpcSettingsSchemaResult {
	entries: RpcSettingEntry[];
	tabs: Array<{
		id: string;
		label: string;
		groups: string[];
	}>;
}
/** A configured model provider and its auth/config state. */
export interface RpcProviderInfo {
	id: string;
	name: string;
	/** True when a credential (OAuth or API key) is present. */
	authenticated: boolean;
	/** Auth kind when authenticated. */
	authKind?: "oauth" | "apikey" | "env";
	/** Account identity (email/org) for OAuth providers. */
	account?: string;
	/** True when the provider has a registered interactive credential flow. */
	loginAvailable: boolean;
	/** True when `set_api_key` can sign the provider in: it also reads an API key from the environment. */
	apiKeyLogin?: boolean;
	/** True when the provider is disabled in settings. */
	disabled: boolean;
	/** Base URL override, when configured. */
	baseUrl?: string;
	/** Number of models currently available from this provider. */
	modelCount: number;
}
/** A model an endpoint offers, as `discover_provider_models` reports it. */
export interface RpcDiscoveredModel {
	id: string;
	name: string;
	reasoning: boolean;
	/** Accepted input modalities, e.g. `text`, `image`. */
	input: string[];
	/** Null when the server does not report a limit. */
	contextWindow: number | null;
	/** Null when the server does not report a limit. */
	maxTokens: number | null;
}
export interface RpcProvidersResult {
	providers: RpcProviderInfo[];
	models: Model[];
	discoveryStates: ProviderDiscoveryState[];
	refreshPending: boolean;
	generation: number;
}
export interface RpcAvailableModelsResult {
	models: Model[];
	discoveryStates: ProviderDiscoveryState[];
	refreshPending: boolean;
	generation: number;
}
/** Emitted after a bounded model-listing response when discovery finishes later. */
export interface RpcModelCatalogUpdateFrame extends RpcAvailableModelsResult {
	type: "model_catalog_update";
	providers: RpcProviderInfo[];
}
export interface RpcPlanModeState {
	enabled: boolean;
	planFilePath?: string;
}
/**
 * Execution option for an approved plan. Mirrors the TUI plan-review choices:
 * `"execute"` starts a fresh session (context cleared), `"compact"` distills
 * the planning transcript first, `"keep_context"` executes on the intact
 * transcript, and `"save"` writes the plan to a host-selected path without
 * starting execution. The TUI's refine choice maps to
 * `plan_approval { approved: false, feedback }` instead of an option here.
 */
export type RpcPlanApprovalOption = "execute" | "compact" | "keep_context" | "save";
export interface RpcPlanApprovalResult {
	approved: boolean;
	/** True when the execution/refinement turn was dispatched to the agent. */
	dispatched: boolean;
	/** Present when nothing was dispatched (plain reject, compaction failure). */
	reason?: string;
	/** Host-selected destination after a successful save-only approval. */
	savedPath?: string;
	/** Whether save-only approval also completed the requested fresh-session transition. */
	freshSessionStarted?: boolean;
}
/**
 * Emitted when the agent submits a plan for approval (a `write` to
 * `xd://propose` while plan mode is active). The host answers with a
 * `plan_approval` command. `options` lists the approval choices; `"refine"`
 * is answered with `approved: false` plus `feedback`, not `option`.
 */
export interface RpcPlanProposalFrame {
	type: "plan_proposal";
	planFilePath: string;
	title: string;
	suggestedFileName: string;
	planContent: string;
	options: string[];
}
export interface RpcVibeModeState {
	enabled: boolean;
	/** Worker sessions killed by a disable transition (omit semantics: only set on set_vibe_mode). */
	killedWorkers?: number;
}
/** Current goal-mode state projected for the wire; absent fields mean "no goal". */
export interface RpcGoalState {
	enabled: boolean;
	status?: GoalStatus;
	objective?: string;
	tokenBudget?: number;
	tokensUsed?: number;
	timeUsedSeconds?: number;
	/** GoalModeState.mode: "active" while running, "exiting" on the completion transition. */
	mode?: "active" | "exiting";
}
export type RpcLoopModeRunState = "off" | "waiting" | "running" | "paused";
export interface RpcLoopModeState {
	enabled: boolean;
	/**
	 * "waiting": enabled, next host prompt becomes the loop prompt. "running": a
	 * loop prompt is captured and re-submits after each yield. "paused": an abort
	 * dropped the current prompt (TUI pauseLoop); the next host prompt re-arms.
	 */
	state: RpcLoopModeRunState;
	prompt?: string;
	limit?: LoopLimitRuntime;
}
/** Emitted on every loop-mode transition, including auto-disable on limit exhaustion. */
export interface RpcLoopModeUpdateFrame {
	type: "loop_mode_update";
	state: RpcLoopModeState;
}
export interface RpcModelRoleCandidate {
	provider: string;
	id: string;
	name: string;
	kind: ModelKind;
	/** Thinking selectors a role assignment can apply to this model, in menu order; empty when it does not reason. */
	thinkingLevels: ConfiguredThinkingLevel[];
}
/** The candidate a role's configured selector names directly, with its `:level` suffix split off. */
export interface RpcModelRoleSelection {
	provider: string;
	id: string;
	/** Explicit `:level` suffix; absent when the selector carries none. */
	thinkingLevel?: ConfiguredThinkingLevel;
}
export interface RpcModelRoleEntry extends RpcModelRoleMetadata {
	/** Configured selector, preserved verbatim even when absent from candidates. */
	model?: string;
	/** Set when `model` names one of `candidates`; absent for aliases, fallback chains and other models. */
	selected?: RpcModelRoleSelection;
	source: string;
	/** Available models accepted by this role, including keyless/local runners. */
	candidates: RpcModelRoleCandidate[];
}
export interface RpcModelRolesResult {
	roles: RpcModelRoleEntry[];
}
export interface RpcModelRoleMetadata {
	id: string;
	name: string;
	tag?: string;
	color?: string;
	hidden?: boolean;
	section: "chat" | "kind";
}
export interface RpcModelRoleMetadataResult {
	roles: RpcModelRoleMetadata[];
}
/** A discoverable skill with its session enable state. */
export interface RpcSkillInfo {
	name: string;
	description: string;
	/** "<provider>:<level>", e.g. "native:project", "claude:user", "custom:user". */
	source: string;
	/** True when the live session actually loaded this skill. */
	enabled: boolean;
	/** Absolute path to the SKILL.md file. */
	location: string;
	/** Discovery provider id, e.g. "native", "codex", or "omp-managed". */
	provider: string;
	/** Human-readable discovery provider name. */
	providerName: string;
	/** Filesystem/config scope reported by capability discovery. */
	level: "user" | "project" | "native";
	/** Only managed skills can be changed or deleted through RPC. */
	managed: boolean;
	/** Managed skills only: last write to SKILL.md, ms since epoch. */
	modifiedAt?: number;
	/** Hidden skills remain manually invocable but are omitted from model discovery. */
	hidden: boolean;
}
export interface RpcSkillsResult {
	skills: RpcSkillInfo[];
}
export interface RpcSkillDetail extends RpcSkillInfo {
	body: string;
}
export interface RpcManageSkillResult {
	action: "create" | "update" | "delete";
	name: string;
}
/** A discovered pre/post tool hook. */
export interface RpcHookInfo {
	/** Stable id used by the `disabledExtensions` setting: "hook:<type>:<tool>:<name>". */
	id: string;
	name: string;
	/** Hook event: "<pre|post>:<tool>", e.g. "pre:bash" or "post:*". */
	event: string;
	enabled: boolean;
	/** Id of the discovery provider that found this hook (e.g. "claude", "codex", "omp-plugins"). */
	source: string;
	/** Absolute path to the hook file. */
	path: string;
}
export interface RpcHooksResult {
	hooks: RpcHookInfo[];
}
/** A configured or discovered MCP server with live connection state. */
export interface RpcMcpServerInfo {
	name: string;
	/** Configured transport; "unknown" when no config exists anywhere (disabled-marker-only entries). */
	transport: "stdio" | "http" | "sse" | "unknown";
	/** Live connection state from the MCP manager ("disconnected" when no manager is running). */
	status: "connected" | "connecting" | "disconnected";
	/** Tools currently exposed by this server (0 when not connected). */
	toolCount: number;
	/** False when disabled in config (`enabled: false`) or via `/mcp disable`. */
	enabled: boolean;
	/** True when stored OAuth credentials or a static Authorization header exist. */
	authed: boolean;
	/** Which writable config file declared the server; omitted for discovered-only entries. */
	scope?: "user" | "project";
	/** Configured stdio command (stdio transports only). */
	command?: string;
	/** Configured URL (http/sse transports only). */
	url?: string;
	/**
	 * Last connection error for this server. Currently always omitted: the MCP
	 * manager surfaces connection failures through transient status events, not
	 * a queryable per-server error store.
	 */
	lastError?: string;
	/**
	 * OAuth credential state derived from the config and stored credentials:
	 * "none" (no OAuth configured), "authorized" (usable credential or static
	 * Authorization header), "expired" (stored credential past its expiry),
	 * "required" (OAuth configured but no usable credential stored).
	 */
	authState?: "none" | "authorized" | "expired" | "required";
}
export interface RpcMcpServersResult {
	servers: RpcMcpServerInfo[];
}
/** An installed plugin (npm package or marketplace install). */
export interface RpcPluginInfo {
	name: string;
	/** Marketplace name for marketplace installs; "npm" for npm package installs. */
	marketplace: string;
	enabled: boolean;
	version: string;
	/** Full "name@marketplace" id (marketplace installs only). */
	id?: string;
	/** Install scope (marketplace installs only). */
	scope?: "user" | "project";
	/** Set when a user-scope install is shadowed by an enabled project-scope install. */
	shadowedBy?: "project";
}
export interface RpcPluginsResult {
	plugins: RpcPluginInfo[];
}
/** Theme tokens contributed by one enabled plugin's gui.theme asset. */
export interface RpcGuiThemeInfo {
	id: string;
	tokens: Record<string, string>;
}
/** Result of `get_gui_themes`: declarative GUI theme tokens from enabled plugins. */
export interface RpcGuiThemesResult {
	themes: RpcGuiThemeInfo[];
}
/** A configured marketplace source. */
export interface RpcMarketplaceInfo {
	name: string;
	/** Source URI (github repo, git/url, or local path). */
	source: string;
	/** Plugins in the cached catalog; omitted until the catalog is fetched (`/marketplace update`). */
	pluginCount?: number;
}
export interface RpcMarketplacesResult {
	marketplaces: RpcMarketplaceInfo[];
}
/** Whether a plugin mutation takes effect live or needs a sidecar restart to load/unload. */
export type RpcPluginActivation = "live" | "restart-required";
/** Result of `set_plugin_enabled`: which install channel persisted the toggle. */
export interface RpcPluginSetEnabledResult {
	id: string;
	enabled: boolean;
	channel: "npm" | "marketplace";
	/** Executable plugins report restart-required in both directions (load/unload). */
	activation?: RpcPluginActivation;
}
/** Result of `mcp_action`; `status` reports the live connection state when a manager is running. */
export interface RpcMcpActionResult {
	name: string;
	action: "enable" | "disable" | "reconnect" | "remove";
	status?: "connected" | "connecting" | "disconnected";
}
/**
 * Wire form of one MCP server definition (`mcp_add.config`, `mcp_test.config`).
 * Maps onto {@link MCPServerConfig}: stdio takes command/args/env, http/sse
 * take url/headers, `timeoutMs` maps to the config's `timeout`.
 */
export interface RpcMcpServerInput {
	transport: "stdio" | "http" | "sse";
	/** stdio: executable to spawn. */
	command?: string;
	/** stdio: argv. */
	args?: string[];
	/** stdio: environment overrides. */
	env?: Record<string, string>;
	/** http/sse: server URL. */
	url?: string;
	/** http/sse: extra headers. */
	headers?: Record<string, string>;
	/** MCP request timeout in milliseconds (maps to the config's `timeout`). */
	timeoutMs?: number;
}
/** Result of `mcp_add`: the refreshed live view of the just-added server. */
export interface RpcMcpAddResult {
	added: true;
	server: RpcMcpServerInfo;
}
/**
 * Result of `mcp_test`. Probe failures (connect/handshake/tool-list, unknown
 * server name, invalid arguments) ride `error` with `ok:false`.
 */
export interface RpcMcpTestResult {
	ok: boolean;
	toolNames?: string[];
	toolCount?: number;
	error?: string;
}
/**
 * Result of `mcp_reauth`. User cancellation resolves `{ ok:false, error:"cancelled" }`;
 * a second reauth claimed while one is in flight is refused at the envelope
 * (`success:false, code:"oauth_busy"`), not here.
 */
export interface RpcMcpReauthResult {
	ok: boolean;
	error?: string;
}
/** Result of `mcp_reauth_cancel`: false when no reauth for that name was in flight. */
export interface RpcMcpReauthCancelResult {
	cancelled: boolean;
}
/** Where a catalog plugin's files come from, as the catalog declares it. A string is a path inside the marketplace. */
export type RpcMarketplacePluginSource = string | {
	source: "github";
	repo: string;
	ref?: string;
	sha?: string;
} | {
	source: "url";
	url: string;
	ref?: string;
	sha?: string;
} | {
	source: "git-subdir";
	url: string;
	path: string;
	ref?: string;
	sha?: string;
} | {
	source: "npm";
	package: string;
	version?: string;
	registry?: string;
};
/** Components a catalog entry declares itself, in the catalog's own shape. The plugin's files can add more. */
export interface RpcMarketplacePluginComponents {
	commands?: unknown;
	agents?: unknown;
	skills?: unknown;
	hooks?: unknown;
	mcpServers?: unknown;
	lspServers?: unknown;
}
/** One catalog entry from a marketplace, with its local install state. */
export interface RpcMarketplacePluginInfo {
	name: string;
	description?: string;
	version?: string;
	installed: boolean;
	/** Catalog metadata; each field is absent when the catalog entry omits it. */
	displayName?: string;
	author?: string;
	license?: string;
	repository?: string;
	homepage?: string;
	category?: string;
	tags?: string[];
	source?: RpcMarketplacePluginSource;
	components?: RpcMarketplacePluginComponents;
}
/**
 * Result of `marketplace_action`. Domain failures (unknown marketplace/plugin,
 * invalid source, install errors) ride `error` with `ok:false`. `plugins` is
 * populated only for `action:"list_available"`; catalogs are cache-backed
 * (populated by `add`/`update`), so the listing reflects the last fetch.
 */
export interface RpcMarketplaceActionResult {
	ok: boolean;
	error?: string;
	plugins?: RpcMarketplacePluginInfo[];
	/** install/upgrade verdict: restart-required when the plugin ships executable entry points. */
	activation?: RpcPluginActivation;
}
/**
 * Full detail for one plugin: manifest-declared features with live enable
 * state, the declared settings schema (omitted when the manifest declares
 * none), non-secret effective setting values (project overrides merged over
 * user), and the keys with persisted values. Secret values never cross the
 * RPC boundary; `configuredKeys` lets clients render their write-only state.
 */
export interface RpcPluginDetail {
	id: string;
	enabled: boolean;
	features: Array<{
		id: string;
		description?: string;
		enabled: boolean;
	}>;
	settingsSchema?: unknown;
	values: Record<string, unknown>;
	configuredKeys: string[];
}
/**
 * Result of the plugin feature/setting mutations. Validation feedback (unknown
 * feature, schema violation) rides `error` with `ok:false`.
 */
export interface RpcPluginMutationResult {
	ok: boolean;
	error?: string;
	/** Feature mutations that (de)activate executable entry points report restart-required. */
	activation?: RpcPluginActivation;
}
/** A file-based prompt template. */
export interface RpcPromptTemplateInfo {
	name: string;
	description: string;
	/** "(user)", "(project)", or a scoped variant like "(project:frontend)". */
	source: string;
	/** "[arguments]" when the template body consumes args inline; omitted otherwise. */
	argumentHint?: string;
}
export interface RpcPromptTemplatesResult {
	templates: RpcPromptTemplateInfo[];
}
/** Structured memory backend status, projected for the wire (mirrors MemoryBackendStatus). */
export interface RpcMemoryStatus {
	active: boolean;
	writable: boolean;
	searchable: boolean;
	scope?: string;
	retainBank?: string;
	recallBanks?: string[];
	workingCount?: number;
	episodicCount?: number;
	tripleCount?: number;
	lastMemory?: string;
	lastRecall?: boolean;
	database?: string;
	message?: string;
	error?: string;
}
/** One project's local memory as the workbench shows it. */
export type RpcMemoryOverview = MemoryOverview;
/** A workbench edit. Notes and lessons are named by their stored `line`. */
export type RpcMemoryEdit = {
	kind: "note";
	action: "add";
	text: string;
} | {
	kind: "note" | "lesson";
	action: "update";
	line: string;
	text: string;
	context?: string;
} | {
	kind: "note" | "lesson";
	action: "delete";
	line: string;
} | {
	kind: "source";
	action: "forget" | "restore";
	threadId: string;
} | {
	kind: "erase";
};
export interface RpcMemoryEditResult {
	/** `ok: false` names an expected refusal (empty text, entry changed elsewhere, notes full). */
	result: MemoryListEditResult;
	overview: RpcMemoryOverview;
}
export interface RpcMemoryUpdateResult {
	/** False when this process was already updating the project. */
	started: boolean;
	overview: RpcMemoryOverview;
}
/** Read-only memory backend report. Optional fields are omitted when the backend lacks (or fails) that hook. */
export interface RpcMemoryReport {
	backend: MemoryBackendId;
	/** working + episodic entry counts, when the backend reports them. */
	entryCount?: number;
	status?: RpcMemoryStatus;
	/** `/memory stats` markdown payload. */
	stats?: string;
	/** `/memory diagnose` markdown payload. */
	diagnosis?: string;
}
export type RpcSecurityDispositionStatus = "open" | "false_positive" | "accepted_risk" | "fixed" | "wont_fix";
export type RpcSecuritySeverityLevel = "critical" | "high" | "medium" | "low" | "informational";
export type RpcSecurityTargetInput = {
	kind: "repository";
} | {
	kind: "working_tree";
} | {
	kind: "ref_diff";
	baseRevision: string;
	headRevision: string;
};
export interface RpcSecurityFindingInfo {
	id: string;
	scanId: string;
	title: string;
	summary: string;
	severity: RpcSecuritySeverityLevel;
	confidence: "high" | "medium" | "low";
	path?: string;
	line?: number;
	disposition: RpcSecurityDispositionStatus;
	validation: "unvalidated" | "validated" | "rejected" | "partial" | "error";
	remediation?: string;
	evidence: Array<{
		label: string;
		explanation: string;
		excerpt?: string;
		path?: string;
		line?: number;
	}>;
}
export interface RpcSecurityScanInfo {
	id: string;
	status: "planned" | "running" | "completed" | "partial" | "cancelled" | "failed";
	createdAt: string;
	completedAt?: string;
	producer: string;
	findingCount: number;
	target: {
		kind: "repository" | "scoped_path" | "ref_diff" | "working_tree" | "imported";
		displayName: string;
		revision?: string;
		baseRevision?: string;
		headRevision?: string;
	};
}
export interface RpcSecurityOperationInfo {
	operationId: string;
	planId: string;
	scanId: string;
	phase: "queued" | "preparing" | "reviewing" | "publishing" | "completed" | "partial" | "cancelled" | "failed";
	createdAt: string;
	updatedAt: string;
	findingCount: number;
	error?: string;
}
export interface RpcSecurityDashboardResult {
	enabled: boolean;
	modelReady: boolean;
	modelLabel?: string;
	repositoryRoot: string;
	revision?: string;
	scans: RpcSecurityScanInfo[];
	operations: RpcSecurityOperationInfo[];
	latest?: RpcSecurityScanResult;
}
export interface RpcSecurityScanResult {
	scan: RpcSecurityScanInfo;
	findings: RpcSecurityFindingInfo[];
}
export interface RpcSshHostInput {
	host: string;
	username?: string;
	port?: number;
	keyPath?: string;
	description?: string;
	compat?: boolean;
}
export interface RpcSshHostInfo extends RpcSshHostInput {
	name: string;
	scope: "user" | "project" | "native";
	editable: boolean;
	source: string;
	os?: "windows" | "linux" | "macos" | "unknown";
	shell?: "cmd" | "powershell" | "bash" | "zsh" | "sh" | "unknown";
	compatShell?: "bash" | "sh";
	transferShell?: "sh" | "bash" | "zsh";
}
export interface RpcSshHostsResult {
	openSshAvailable: boolean;
	hosts: RpcSshHostInfo[];
	warnings: string[];
}
export interface RpcSshTestResult {
	name: string;
	ok: boolean;
	checkedAt: string;
	os?: RpcSshHostInfo["os"];
	shell?: RpcSshHostInfo["shell"];
	compatShell?: RpcSshHostInfo["compatShell"];
	transferShell?: RpcSshHostInfo["transferShell"];
	error?: string;
}
export interface RpcOmpUpdateResult {
	currentVersion: string;
	latestVersion: string;
	updateAvailable: boolean;
	checkedAt: string;
	distribution: "bundled";
	installStrategy: "gui-update";
}
/** A node in the session's branch tree (visual session navigation). */
export interface RpcSessionTreeNode {
	entryId: string;
	/** Nearest ancestor that is itself included in the tree array (null for roots). */
	parentId: string | null;
	role: "user" | "assistant" | "system";
	/** Whitespace-collapsed, pre-truncated preview (~200 chars). */
	textPreview: string;
	/** ms epoch. */
	timestamp: number;
	label?: string;
	/** True when this node lies on the path from root to the active leaf. */
	onActiveBranch: boolean;
	/** True when no other included node lists this as its parent. */
	isLeaf: boolean;
}
export interface RpcSessionTreeResult {
	tree: RpcSessionTreeNode[];
	/** The active leaf, resolved to the nearest included message entry (null when none). */
	activeLeafId: string | null;
}
/** An available UI theme (built-in or custom file). */
export interface RpcThemeInfo {
	name: string;
	path?: string;
}
export interface RpcThemesResult {
	themes: RpcThemeInfo[];
}
/** A UI theme's resolved colors as CSS hex strings, keyed by theme token (accent, statusLineBg, …). */
export interface RpcThemeColorsResult {
	name: string;
	colors: Record<string, string>;
}
export type RpcSubagentSubscriptionLevel = "off" | "progress" | "events";
export interface RpcSubagentSnapshot {
	id: string;
	index: number;
	agent: string;
	agentSource?: AgentProgress["agentSource"];
	description?: string;
	status: string;
	/** False when a registry ref claims running but has no turn in flight. */
	live?: boolean;
	task?: string;
	assignment?: string;
	sessionFile?: string;
	lastUpdate: number;
	/** When the agent first started, ms since the epoch; a revival keeps the first start. */
	spawnedAt?: number;
	progress?: AgentProgress;
	parentToolCallId?: string;
	/** Registry id of the spawning subagent for nested spawns; absent at the root. */
	parentSubagentId?: string;
	kind?: "sub" | "advisor";
}
export interface RpcAgentDefinitionInfo {
	name: string;
	description: string;
	filePath?: string;
	model?: string[];
	thinkingLevel?: string;
	tools?: string[];
	spawns?: string[] | "*";
	autoloadSkills?: string[];
	output?: unknown;
	blocking?: boolean;
	readSummarize?: boolean;
	prewalk?: boolean | string;
	defaultPatterns: string[];
	defaultResolved?: string;
	effectivePatterns: string[];
	effectiveResolved?: string;
	effectiveThinkingLevel?: string;
	prewalkPattern?: string;
	prewalkResolved?: string;
	source: "bundled" | "user" | "project";
}
export interface RpcAgentDefinitionsResult {
	agents: RpcAgentDefinitionInfo[];
}
export interface RpcSubagentMessagesResult {
	sessionFile: string;
	fromByte: number;
	nextByte: number;
	reset: boolean;
	entries: FileEntry[];
	messages: AgentMessage[];
}
/** One session workspace root. Exactly one entry — the session cwd — is primary. */
export interface RpcWorkspaceDirectory {
	path: string;
	primary: boolean;
}
/**
 * Result of get_directories / add_directory / remove_directory: the session's
 * workspace roots, primary (cwd) first, additional directories in their
 * stored order.
 */
export interface RpcWorkspaceDirectoriesResult {
	directories: RpcWorkspaceDirectory[];
}
/**
 * Result of get_git_status: the footer git segment's live state for the
 * session cwd (TUI gitSegment parity). `isRepo` false outside a repository
 * (counts zeroed, branch null); `branch` null when detached.
 */
/** Net changes from the current HEAD to this checkout, including untracked files. */
export interface RpcGitChanges {
	isRepo: boolean;
	root: string | null;
	base: string | null;
	files: {
		path: string;
		oldPath?: string;
		status: string;
	}[];
	truncated: boolean;
}
export interface RpcGitDiff {
	path: string;
	diff: string;
	kind: "text" | "binary" | "large" | "symlink" | "directory";
	truncated: boolean;
}
export interface RpcGitStatus {
	isRepo: boolean;
	branch: string | null;
	staged: number;
	unstaged: number;
	untracked: number;
}
/**
 * Result of worktree_create: the materialized worktree. `path` is the
 * checkout root (~/.omp/wt/gui-<name>-<hash7>), `branch` the new
 * omp/gui/<name> ref, `baseCwd` the repo's primary checkout it forked from.
 */
export interface RpcWorktreeCreateResult {
	path: string;
	branch: string;
	baseCwd: string;
}
/** pr_repo: gh availability + the session cwd's GitHub repo, or the typed reason it's unusable. */
export type RpcPrRepo = {
	available: true;
	repo: string;
	defaultBranch: string | null;
} | {
	available: false;
	reason: "gh_missing" | "not_a_repo" | "no_github_remote";
};
/** pr_list row: one PR with rollup CI counts (success/failure/pending). */
export interface RpcPrListItem {
	number: number;
	title: string;
	url: string;
	isDraft: boolean;
	authorLogin: string;
	headRefName: string;
	baseRefName: string;
	additions: number;
	deletions: number;
	updatedAt: string;
	reviewDecision: string | null;
	checks: {
		success: number;
		failure: number;
		pending: number;
	};
}
/** pr_get detail: everything the PR Center renders except per-file diff text. */
export interface RpcPrDetail {
	number: number;
	title: string;
	url: string;
	isDraft: boolean;
	authorLogin: string;
	body: string;
	baseRefName: string;
	headRefName: string;
	mergeStateStatus: string;
	additions: number;
	deletions: number;
	reviewDecision: string | null;
	files: Array<{
		path: string;
		changeType: string;
		additions: number;
		deletions: number;
	}>;
	checks: Array<{
		name: string;
		status: string;
		conclusion: string | null;
	}>;
}
/** pr_draft result (AI-drafted, user-editable before pr_create). */
export interface RpcPrDraftResult {
	title: string;
	body: string;
}
/** pr_create result. */
export interface RpcPrCreateResult {
	url: string;
	number: number;
}
/**
 * Result of get_context_report: the provider-anchored token breakdown from
 * `session.getContextBreakdown()` verbatim, plus the header fields the TUI
 * renders above it. `contextWindow` is 0 (and `model` empty) when no model
 * is selected — the TUI's "unavailable" state.
 */
export interface RpcContextReportResult {
	breakdown: ContextUsageBreakdown | undefined;
	contextWindow: number;
	/** Active model id; empty when no model is selected. */
	model: string;
}
/** Tool provenance bucket. `plugin` is reserved: plugin-shipped tools load
 * through the extension/custom-tool path and surface as `extension`. */
export type RpcToolSource = "builtin" | "mcp" | "extension" | "plugin";
export interface RpcActiveTool {
	name: string;
	description?: string;
	source: RpcToolSource;
}
/**
 * Result of get_active_tools: active top-level tools (TUI /tools rows),
 * followed by xd:// mounted entries (the `~ xd://name` rows).
 */
export interface RpcActiveToolsResult {
	tools: RpcActiveTool[];
}
/** Result of share_session. `truncated` rides only when content was trimmed. */
export interface RpcShareSessionPreview {
	/** The local preview already reflects upload size limits. */
	truncated?: boolean;
	snapshotId: string;
	preview: string;
	serverUrl: string;
	store: "blob" | "gist";
	redactionEnabled: boolean;
}
export interface RpcShareSessionResult {
	url: string;
	truncated?: boolean;
}
/**
 * One get_jobs tree row: a job (a task job also heads the subagent it runs), or
 * a header for a subagent whose task job is no longer listed.
 */
export interface RpcJobTreeRow {
	/** 0 for the viewing agent's own jobs; one more per subagent level. */
	depth: number;
	job?: AsyncJobSnapshotItem;
	/** Registry id of the subagent this row heads. */
	agentId?: string;
}
/**
 * Result of get_jobs: running jobs first, then recent (TUI /jobs ordering),
 * items as-is from the session's async-job snapshot. Empty when the session
 * has no job registry.
 */
export interface RpcJobsResult {
	jobs: AsyncJobSnapshotItem[];
	/** This agent's and its subagents' jobs nested as the TUI /jobs tree shows them, in display order. */
	tree?: RpcJobTreeRow[];
}
/** Result of set_prewalk: the armed state after the toggle. */
export interface RpcPrewalkState {
	enabled: boolean;
}
/** Result of fresh (provider stream state reset); deliberately empty. */
export type RpcFreshResult = Record<string, never>;
/** Result of shake_context. `removed` is the one-line operator summary (TUI formatShakeSummary). */
export interface RpcShakeContextResult {
	removed: string;
}
/** Result of reload_plugins: post-reload inventory counts. */
export interface RpcReloadPluginsResult {
	plugins: number;
	skills: number;
	commands: number;
}
/** Result of set_force_tool / get_force_tool: the pending forced tool, or null. */
export interface RpcForceToolState {
	tool: string | null;
}
export type RpcResponse = {
	id?: string;
	type: "response";
	command: "negotiate_protocol";
	success: true;
	data: {
		protocolVersion: 2;
	};
} | {
	id?: string;
	type: "response";
	command: "prompt";
	success: true;
	data?: {
		agentInvoked: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "steer";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "follow_up";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "remove_queued_message";
	success: true;
	data: RpcRemoveQueuedMessageResult;
} | {
	id?: string;
	type: "response";
	command: "promote_queued_message";
	success: true;
	data: {
		promoted: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "abort";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "abort_and_prompt";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "abort_and_restore_queue";
	success: true;
	data: RpcAbortAndRestoreQueueResult;
} | {
	id?: string;
	type: "response";
	command: "new_session";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "drop_session";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "open_session";
	success: true;
	data: RpcOpenSessionResult;
} | {
	id?: string;
	type: "response";
	command: "get_state";
	success: true;
	data: RpcSessionState;
} | {
	id?: string;
	type: "response";
	command: "set_fast_mode";
	success: true;
	data: {
		enabled: boolean;
		active: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "set_slow_mode";
	success: true;
	data: {
		enabled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "goal";
	success: true;
	data: RpcGoalResult;
} | {
	id?: string;
	type: "response";
	command: "set_ask_dialog";
	success: true;
	data: {
		enabled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "get_available_commands";
	success: true;
	data: {
		commands: RpcAvailableSlashCommand[];
	};
} | {
	id?: string;
	type: "response";
	command: "get_entries";
	success: true;
	data: {
		entries: SessionEntry[];
		leafId: string | null;
	};
} | {
	id?: string;
	type: "response";
	command: "get_tree";
	success: true;
	data: {
		tree: SessionTreeNode[];
		leafId: string | null;
	};
} | {
	id?: string;
	type: "response";
	command: "set_todos";
	success: true;
	data: {
		todoPhases: TodoPhase[];
	};
} | {
	id?: string;
	type: "response";
	command: "set_host_tools";
	success: true;
	data: {
		toolNames: string[];
	};
} | {
	id?: string;
	type: "response";
	command: "set_host_uri_schemes";
	success: true;
	data: {
		schemes: string[];
	};
} | {
	id?: string;
	type: "response";
	command: "set_event_filter";
	success: true;
	data: {
		events: string[] | null;
		messageUpdates: RpcMessageUpdates;
	};
} | {
	id?: string;
	type: "response";
	command: "set_subagent_subscription";
	success: true;
	data: {
		level: RpcSubagentSubscriptionLevel;
	};
} | {
	id?: string;
	type: "response";
	command: "get_subagents";
	success: true;
	data: {
		subagents: RpcSubagentSnapshot[];
	};
} | {
	id?: string;
	type: "response";
	command: "get_subagent_messages";
	success: true;
	data: RpcSubagentMessagesResult;
} | {
	id?: string;
	type: "response";
	command: "cancel_subagent";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "steer_subagent";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "set_model";
	success: true;
	data: Model;
} | {
	id?: string;
	type: "response";
	command: "cycle_model";
	success: true;
	data: {
		model: Model;
		thinkingLevel: ThinkingLevel | undefined;
		isScoped: boolean;
	} | null;
} | {
	id?: string;
	type: "response";
	command: "get_available_models";
	success: true;
	data: RpcAvailableModelsResult;
} | {
	id?: string;
	type: "response";
	command: "discover_provider_models";
	success: true;
	data: {
		models: RpcDiscoveredModel[];
	};
} | {
	id?: string;
	type: "response";
	command: "set_thinking_level";
	success: true;
	data: RpcThinkingLevelState;
} | {
	id?: string;
	type: "response";
	command: "cycle_thinking_level";
	success: true;
	data: {
		level: Effort;
	} | null;
} | {
	id?: string;
	type: "response";
	command: "get_available_thinking_levels";
	success: true;
	data: {
		levels: ThinkingLevel[];
	};
} | {
	id?: string;
	type: "response";
	command: "set_steering_mode";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "set_follow_up_mode";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "set_interrupt_mode";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "dequeue";
	success: true;
	data: RpcDequeueResult;
} | {
	id?: string;
	type: "response";
	command: "compact";
	success: true;
	data: CompactionResult;
} | {
	id?: string;
	type: "response";
	command: "set_auto_compaction";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "set_cache_warming";
	success: true;
	data: {
		mode: CacheWarmingMode;
	};
} | {
	id?: string;
	type: "response";
	command: "set_auto_retry";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "abort_retry";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "retry";
	success: true;
	data: {
		retried: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "clear_context";
	success: true;
	data: {
		cleared: boolean;
		droppedCount?: number;
	};
} | {
	id?: string;
	type: "response";
	command: "abort_subagent";
	success: true;
	data: {
		ok: boolean;
		reason?: string;
	};
} | {
	id?: string;
	type: "response";
	command: "revive_subagent";
	success: true;
	data: {
		ok: boolean;
		reason?: string;
	};
} | {
	id?: string;
	type: "response";
	command: "write_local_paste";
	success: true;
	data: {
		name: string;
		url: string;
	};
} | {
	id?: string;
	type: "response";
	command: "list_foreign_sessions";
	success: true;
	data: {
		sessions: RpcForeignSessionInfo[];
	};
} | {
	id?: string;
	type: "response";
	command: "import_foreign_session";
	success: true;
	data: {
		sessionPath: string;
		sessionId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "get_command_arg_completions";
	success: true;
	data: {
		items: Array<{
			value: string;
			label?: string;
			description?: string;
			hint?: string;
		}>;
	};
} | {
	id?: string;
	type: "response";
	command: "bash";
	success: true;
	data: BashResult;
} | {
	id?: string;
	type: "response";
	command: "abort_bash";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "eval";
	success: true;
	data: RpcEvalResult;
} | {
	id?: string;
	type: "response";
	command: "abort_eval";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "get_chat_config";
	success: true;
	data: RpcChatConfig;
} | {
	id?: string;
	type: "response";
	command: "set_chat_config";
	success: true;
	data: RpcChatConfig;
} | {
	id?: string;
	type: "response";
	command: "get_session_stats";
	success: true;
	data: SessionStats;
} | {
	id?: string;
	type: "response";
	command: "get_session_lease";
	success: true;
	data: SessionLeaseReport;
} | {
	id?: string;
	type: "response";
	command: "set_session_pinned";
	success: true;
	data: {
		pinned: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "export_html";
	success: true;
	data: {
		path: string;
	};
} | {
	id?: string;
	type: "response";
	command: "switch_session";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "branch";
	success: true;
	data: {
		text: string;
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "fork";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "fork_from";
	success: true;
	data: {
		sessionPath: string;
		sessionId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "switch_leaf";
	success: true;
	data: RpcSwitchLeafResult;
} | {
	id?: string;
	type: "response";
	command: "resume_after_ask_reanswer";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "navigate_tree";
	success: true;
	data: RpcNavigateTreeResult;
} | {
	id?: string;
	type: "response";
	command: "abort_branch_summary";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "set_label";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "get_branch_messages";
	success: true;
	data: {
		messages: Array<{
			entryId: string;
			text: string;
		}>;
	};
} | {
	id?: string;
	type: "response";
	command: "get_config_warnings";
	success: true;
	data: {
		warnings: string[];
	};
} | {
	id?: string;
	type: "response";
	command: "get_advisor_status";
	success: true;
	data: RpcAdvisorStatus;
} | {
	id?: string;
	type: "response";
	command: "list_reset_credits";
	success: true;
	data: {
		accounts: RpcResetCreditAccount[];
	};
} | {
	id?: string;
	type: "response";
	command: "redeem_reset_credit";
	success: true;
	data: RpcResetCreditRedeemResult;
} | {
	id?: string;
	type: "response";
	command: "get_last_assistant_text";
	success: true;
	data: {
		text: string | null;
	};
} | {
	id?: string;
	type: "response";
	command: "get_copy_targets";
	success: true;
	data: {
		targets: CopyTarget[];
	};
} | {
	id?: string;
	type: "response";
	command: "guided_goal";
	success: true;
	data: {
		started: true;
	};
} | {
	id?: string;
	type: "response";
	command: "btw_branch";
	success: true;
	data: {
		cancelled: boolean;
		sessionFile?: string;
	};
} | {
	id?: string;
	type: "response";
	command: "tan";
	success: true;
	data: {
		jobId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "omfg";
	success: true;
	data: {
		state: "saved" | "rejected" | "aborted";
		savedPath?: string;
	};
} | {
	id?: string;
	type: "response";
	command: "set_agents_paused";
	success: true;
	data: {
		paused: boolean;
		pausedAt?: number;
		heldMs?: number;
	};
} | {
	id?: string;
	type: "response";
	command: "set_session_name";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "set_entry_label";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "handoff";
	success: true;
	data: RpcHandoffResult | null;
} | {
	id?: string;
	type: "response";
	command: "live_start";
	success: true;
	data: {
		voice: string;
	};
} | {
	id?: string;
	type: "response";
	command: "live_connect";
	success: true;
	data: {
		voice: string;
		sdpAnswer: string;
	};
} | {
	id?: string;
	type: "response";
	command: "live_stop";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "host_dictation_start";
	success: true;
	data: {
		state: RpcHostDictationState;
	};
} | {
	id?: string;
	type: "response";
	command: "host_dictation_audio";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "host_dictation_stop";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "host_dictation_cancel";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "terminal_open";
	success: true;
	data: RpcTerminalInfo;
} | {
	id?: string;
	type: "response";
	command: "terminal_run_cli";
	success: true;
	data: RpcTerminalInfo;
} | {
	id?: string;
	type: "response";
	command: "tui_open";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "terminal_input";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "terminal_resize";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "terminal_close";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "terminal_list";
	success: true;
	data: {
		terminals: RpcTerminalInfo[];
	};
} | {
	id?: string;
	type: "response";
	command: "terminal_snapshot";
	success: true;
	data: RpcTerminalSnapshot;
} | {
	id?: string;
	type: "response";
	command: "terminal_configure";
	success: true;
	data: {
		interactiveCommands: boolean;
		agentTools: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "browser_host_start";
	success: true;
	data: RpcBrowserHostInfo;
} | {
	id?: string;
	type: "response";
	command: "browser_host_stop";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "live_mute";
	success: true;
	data: {
		muted: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "dictation_start";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "dictation_stop";
	success: true;
	data: {
		text: string;
	};
} | {
	id?: string;
	type: "response";
	command: "handoff";
	success: true;
	data: RpcHandoffResult | null;
} | {
	id?: string;
	type: "response";
	command: "get_messages";
	success: true;
	data: {
		messages: AgentMessage[];
	};
} | {
	id?: string;
	type: "response";
	command: "get_messages_page";
	success: true;
	data: RpcMessagesPage;
} | {
	id?: string;
	type: "response";
	command: "get_transcript_page";
	success: true;
	data: RpcMessagesPage;
} | {
	id?: string;
	type: "response";
	command: "get_login_providers";
	success: true;
	data: {
		providers: Array<{
			id: string;
			name: string;
			available: boolean;
			authenticated: boolean;
		}>;
	};
} | {
	id?: string;
	type: "response";
	command: "login";
	success: true;
	data: {
		providerId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "logout";
	success: true;
	data: {
		providerId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "get_usage";
	success: true;
	data: RpcUsageResult;
} | {
	id?: string;
	type: "response";
	command: "get_settings_schema";
	success: true;
	data: RpcSettingsSchemaResult;
} | {
	id?: string;
	type: "response";
	command: "get_settings";
	success: true;
	data: {
		values: Record<string, unknown>;
		provenance?: Record<string, RpcSettingProvenance>;
		advisorEnabled: boolean;
		advisorActive: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "set_setting";
	success: true;
	data: {
		path: string;
		value: unknown;
		savedValue?: unknown;
		provenance?: RpcSettingProvenance;
		advisorEnabled?: boolean;
		advisorActive?: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "get_providers";
	success: true;
	data: RpcProvidersResult;
} | {
	id?: string;
	type: "response";
	command: "set_plan_mode";
	success: true;
	data: RpcPlanModeState;
} | {
	id?: string;
	type: "response";
	command: "get_plan_mode";
	success: true;
	data: RpcPlanModeState;
} | {
	id?: string;
	type: "response";
	command: "plan_approval";
	success: true;
	data: RpcPlanApprovalResult;
} | {
	id?: string;
	type: "response";
	command: "get_vibe_mode";
	success: true;
	data: RpcVibeModeState;
} | {
	id?: string;
	type: "response";
	command: "set_vibe_mode";
	success: true;
	data: RpcVibeModeState;
} | {
	id?: string;
	type: "response";
	command: "get_goal";
	success: true;
	data: RpcGoalState;
} | {
	id?: string;
	type: "response";
	command: "set_goal";
	success: true;
	data: RpcGoalState;
} | {
	id?: string;
	type: "response";
	command: "get_loop_mode";
	success: true;
	data: RpcLoopModeState;
} | {
	id?: string;
	type: "response";
	command: "set_loop_mode";
	success: true;
	data: RpcLoopModeState;
} | {
	id?: string;
	type: "response";
	command: "get_model_roles";
	success: true;
	data: RpcModelRolesResult;
} | {
	id?: string;
	type: "response";
	command: "set_model_role";
	success: true;
	/** `modelId` is the stored role value, `:level` suffix included. */
	data: {
		role: string;
		modelId: string | null;
	};
} | {
	id?: string;
	type: "response";
	command: "get_model_role_metadata";
	success: true;
	data: RpcModelRoleMetadataResult;
} | {
	id?: string;
	type: "response";
	command: "get_skills";
	success: true;
	data: RpcSkillsResult;
} | {
	id?: string;
	type: "response";
	command: "get_skill_detail";
	success: true;
	data: RpcSkillDetail;
} | {
	id?: string;
	type: "response";
	command: "get_agent_definitions";
	success: true;
	data: RpcAgentDefinitionsResult;
} | {
	id?: string;
	type: "response";
	command: "get_hooks";
	success: true;
	data: RpcHooksResult;
} | {
	id?: string;
	type: "response";
	command: "get_mcp_servers";
	success: true;
	data: RpcMcpServersResult;
} | {
	id?: string;
	type: "response";
	command: "get_plugins";
	success: true;
	data: RpcPluginsResult;
} | {
	id?: string;
	type: "response";
	command: "get_marketplaces";
	success: true;
	data: RpcMarketplacesResult;
} | {
	id?: string;
	type: "response";
	command: "get_gui_themes";
	success: true;
	data: RpcGuiThemesResult;
} | {
	id?: string;
	type: "response";
	command: "get_prompt_templates";
	success: true;
	data: RpcPromptTemplatesResult;
} | {
	id?: string;
	type: "response";
	command: "get_memory_report";
	success: true;
	data: RpcMemoryReport;
} | {
	id?: string;
	type: "response";
	command: "get_memory_overview";
	success: true;
	data: RpcMemoryOverview;
} | {
	id?: string;
	type: "response";
	command: "get_memory_activity";
	success: true;
	data: Pick<RpcMemoryOverview, "activity">;
} | {
	id?: string;
	type: "response";
	command: "edit_memory";
	success: true;
	data: RpcMemoryEditResult;
} | {
	id?: string;
	type: "response";
	command: "update_memory";
	success: true;
	data: RpcMemoryUpdateResult;
} | {
	id?: string;
	type: "response";
	command: "get_security_dashboard";
	success: true;
	data: RpcSecurityDashboardResult;
} | {
	id?: string;
	type: "response";
	command: "get_security_scan";
	success: true;
	data: RpcSecurityScanResult;
} | {
	id?: string;
	type: "response";
	command: "get_ssh_hosts";
	success: true;
	data: RpcSshHostsResult;
} | {
	id?: string;
	type: "response";
	command: "get_omp_update";
	success: true;
	data: RpcOmpUpdateResult;
} | {
	id?: string;
	type: "response";
	command: "get_context_report";
	success: true;
	data: RpcContextReportResult;
} | {
	id?: string;
	type: "response";
	command: "get_active_tools";
	success: true;
	data: RpcActiveToolsResult;
} | {
	id?: string;
	type: "response";
	command: "preview_share_session";
	success: true;
	data: RpcShareSessionPreview;
} | {
	id?: string;
	type: "response";
	command: "share_session";
	success: true;
	data: RpcShareSessionResult;
} | {
	id?: string;
	type: "response";
	command: "get_jobs";
	success: true;
	data: RpcJobsResult;
} | {
	id?: string;
	type: "response";
	command: "set_prewalk";
	success: true;
	data: RpcPrewalkState;
} | {
	id?: string;
	type: "response";
	command: "fresh";
	success: true;
	data: RpcFreshResult;
} | {
	id?: string;
	type: "response";
	command: "shake_context";
	success: true;
	data: RpcShakeContextResult;
} | {
	id?: string;
	type: "response";
	command: "reload_plugins";
	success: true;
	data: RpcReloadPluginsResult;
} | {
	id?: string;
	type: "response";
	command: "set_force_tool";
	success: true;
	data: RpcForceToolState;
} | {
	id?: string;
	type: "response";
	command: "get_force_tool";
	success: true;
	data: RpcForceToolState;
} | {
	id?: string;
	type: "response";
	command: "set_skill_enabled";
	success: true;
	data: {
		name: string;
		enabled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "manage_skill";
	success: true;
	data: RpcManageSkillResult;
} | {
	id?: string;
	type: "response";
	command: "set_hook_enabled";
	success: true;
	data: {
		id: string;
		enabled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "set_plugin_enabled";
	success: true;
	data: RpcPluginSetEnabledResult;
} | {
	id?: string;
	type: "response";
	command: "mcp_action";
	success: true;
	data: RpcMcpActionResult;
} | {
	id?: string;
	type: "response";
	command: "security_start";
	success: true;
	data: RpcSecurityOperationInfo;
} | {
	id?: string;
	type: "response";
	command: "security_cancel";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "security_validate";
	success: true;
	data: {
		accepted: true;
	};
} | {
	id?: string;
	type: "response";
	command: "security_set_disposition";
	success: true;
	data: RpcSecurityFindingInfo;
} | {
	id?: string;
	type: "response";
	command: "ssh_manage";
	success: true;
	data: RpcSshHostInfo | {
		deleted: true;
	};
} | {
	id?: string;
	type: "response";
	command: "ssh_test";
	success: true;
	data: RpcSshTestResult;
} | {
	id?: string;
	type: "response";
	command: "get_session_tree";
	success: true;
	data: RpcSessionTreeResult;
} | {
	id?: string;
	type: "response";
	command: "get_themes";
	success: true;
	data: RpcThemesResult;
} | {
	id?: string;
	type: "response";
	command: "get_theme_colors";
	success: true;
	data: RpcThemeColorsResult;
} | {
	id?: string;
	type: "response";
	command: "get_transcript";
	success: true;
	data: {
		messages: AgentMessage[];
	};
} | {
	id?: string;
	type: "response";
	command: "transcribe_audio";
	success: true;
	data: {
		text: string;
	};
} | {
	id?: string;
	type: "response";
	command: "synthesize_speech";
	success: true;
	data: {
		audioBase64: string;
		mimeType: string;
	};
} | {
	id?: string;
	type: "response";
	command: "mcp_add";
	success: true;
	data: RpcMcpAddResult;
} | {
	id?: string;
	type: "response";
	command: "mcp_test";
	success: true;
	data: RpcMcpTestResult;
} | {
	id?: string;
	type: "response";
	command: "mcp_reauth";
	success: true;
	data: RpcMcpReauthResult;
} | {
	id?: string;
	type: "response";
	command: "mcp_reauth_cancel";
	success: true;
	data: RpcMcpReauthCancelResult;
} | {
	id?: string;
	type: "response";
	command: "marketplace_action";
	success: true;
	data: RpcMarketplaceActionResult;
} | {
	id?: string;
	type: "response";
	command: "get_plugin_detail";
	success: true;
	data: RpcPluginDetail;
} | {
	id?: string;
	type: "response";
	command: "set_plugin_features";
	success: true;
	data: RpcPluginMutationResult;
} | {
	id?: string;
	type: "response";
	command: "set_plugin_setting";
	success: true;
	data: RpcPluginMutationResult;
} | {
	id?: string;
	type: "response";
	command: "delete_plugin_setting";
	success: true;
	data: RpcPluginMutationResult;
} | {
	id?: string;
	type: "response";
	command: "debug";
	success: true;
	data: {
		content: unknown;
		details?: unknown;
	};
} | {
	id?: string;
	type: "response";
	command: "collab_start";
	success: true;
	data: RpcCollabState;
} | {
	id?: string;
	type: "response";
	command: "collab_join";
	success: true;
	data: RpcCollabState;
} | {
	id?: string;
	type: "response";
	command: "collab_leave";
	success: true;
	data: RpcCollabState;
} | {
	id?: string;
	type: "response";
	command: "get_collab_state";
	success: true;
	data: RpcCollabState;
} | {
	id?: string;
	type: "response";
	command: "get_directories";
	success: true;
	data: RpcWorkspaceDirectoriesResult;
} | {
	id?: string;
	type: "response";
	command: "add_directory";
	success: true;
	data: RpcWorkspaceDirectoriesResult;
} | {
	id?: string;
	type: "response";
	command: "remove_directory";
	success: true;
	data: RpcWorkspaceDirectoriesResult;
} | {
	id?: string;
	type: "response";
	command: "move_session";
	success: true;
	data: {
		cwd: string;
	};
} | {
	id?: string;
	type: "response";
	command: "get_git_status";
	success: true;
	data: RpcGitStatus;
} | {
	id?: string;
	type: "response";
	command: "get_git_changes";
	success: true;
	data: RpcGitChanges;
} | {
	id?: string;
	type: "response";
	command: "get_git_diff";
	success: true;
	data: RpcGitDiff;
} | {
	id?: string;
	type: "response";
	command: "worktree_create";
	success: true;
	data: RpcWorktreeCreateResult;
} | {
	id?: string;
	type: "response";
	command: "worktree_remove";
	success: true;
	data: {
		removed: true;
	};
} | {
	id?: string;
	type: "response";
	command: "pr_repo";
	success: true;
	data: RpcPrRepo;
} | {
	id?: string;
	type: "response";
	command: "pr_list";
	success: true;
	data: RpcPrListItem[];
} | {
	id?: string;
	type: "response";
	command: "pr_get";
	success: true;
	data: RpcPrDetail;
} | {
	id?: string;
	type: "response";
	command: "pr_diff";
	success: true;
	data: {
		diff: string;
	};
} | {
	id?: string;
	type: "response";
	command: "pr_draft";
	success: true;
	data: RpcPrDraftResult;
} | {
	id?: string;
	type: "response";
	command: "pr_create";
	success: true;
	data: RpcPrCreateResult;
} | {
	id?: string;
	type: "response";
	command: "pr_checkout";
	success: true;
	data: {
		path: string;
		branch: string;
	};
} | {
	id?: string;
	type: "response";
	command: "login";
	success: true;
	data: {
		providerId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "set_api_key";
	success: true;
	data: {
		providerId: string;
	};
} | {
	id?: string;
	type: "response";
	command: "get_logout_accounts";
	success: true;
	data: {
		accounts: LogoutAccount[];
	};
} | {
	id?: string;
	type: "response";
	command: "logout";
	success: true;
	data: {
		remainingSource?: string;
	};
} | {
	id?: string;
	type: "response";
	command: "predict_word";
	success: true;
	data: {
		suffix: string | null;
	};
} | {
	id?: string;
	type: "response";
	command: "predict_word_feedback";
	success: true;
} | {
	id?: string;
	type: "response";
	command: "btw";
	success: true;
	data: {
		record: RpcBtwHistoryRecord;
	};
} | {
	id?: string;
	type: "response";
	command: "btw_cancel";
	success: true;
	data: {
		cancelled: boolean;
	};
} | {
	id?: string;
	type: "response";
	command: "btw_inject";
	success: true;
	data: {
		sent: boolean;
		reason?: string;
	};
} | {
	id?: string;
	type: "response";
	command: "get_btw_history";
	success: true;
	data: {
		records: readonly RpcBtwHistoryRecord[];
	};
} | {
	id?: string;
	type: "response";
	command: string;
	success: false;
	error: string;
	code?: string;
};
/**
 * A BTW topic as sent to RPC hosts. The TUI-only `title` heading is never on the wire,
 * which keeps the released record shape: hosts show the topic's opening question.
 */
export type RpcBtwHistoryRecord = Omit<BtwHistoryRecord, "title"> & {
	/**
	 * The latest turn's `<inject>` block (`btw.inject`): `ready` to send with `btw_inject`,
	 * `sending`, or `sent`. Absent when the turn was not asked with the setting on or drafted none.
	 */
	inject?: RpcBtwInjectState;
};
/** Where a side answer's steering prompt stands; only this process's answers can be sent. */
export type RpcBtwInjectState = "ready" | "sending" | "sent";
/** Text appended to the running side question's latest answer. */
export interface RpcBtwDeltaFrame {
	type: "btw_delta";
	recordId: string;
	delta: string;
}
/** Full record snapshot on every lifecycle change: started, complete, cancelled, error. */
export interface RpcBtwRecordFrame {
	type: "btw_record";
	record: RpcBtwHistoryRecord;
}
export interface RpcSubagentLifecycleFrame {
	type: "subagent_lifecycle";
	payload: SubagentLifecyclePayload;
}
export interface RpcSubagentProgressFrame {
	type: "subagent_progress";
	payload: SubagentProgressPayload;
}
export interface RpcSubagentEventFrame {
	type: "subagent_event";
	payload: SubagentEventPayload;
}
export type RpcSubagentFrame = RpcSubagentLifecycleFrame | RpcSubagentProgressFrame | RpcSubagentEventFrame;
/** Message lifecycle event kinds that RPC mode stamps with a `messageId`. */
export type RpcMessageEventType = "message_start" | "message_update" | "message_end";
/**
 * Message lifecycle frame as written by RPC mode. `messageId` is shared by the
 * `message_start`, every `message_update`, and the `message_end` of one message;
 * it is unique within the RPC process.
 */
export type RpcMessageEventFrame = Extract<AgentSessionEvent, {
	type: RpcMessageEventType;
}> & {
	messageId: string;
};
type WithoutPartial<T> = T extends unknown ? Omit<T, "partial"> : never;
type DeltaOf<Type extends AssistantMessageEvent["type"]> = WithoutPartial<Extract<AssistantMessageEvent, {
	type: Type;
}>>;
/**
 * `assistantMessageEvent` of a delta `message_update`: `*_delta` carries only the increment; a `*_start` carries the
 * shell of the block it opened (`partial.content[contentIndex]` with its text, thinking, or tool arguments emptied, so
 * the deltas that follow apply cleanly); `text_end`/`thinking_end` carry the rest of the block (everything but the text
 * their `content` already holds); `image_end` and `toolcall_end` carry the whole block as `content` / `toolCall`. A
 * re-`start` carries the whole message it restarts from; that snapshot may already be ahead of the stream, so each
 * block's `*_end` is authoritative. `block` is absent only if the provider's `contentIndex` points at no block.
 */
export type RpcDeltaAssistantMessageEvent = DeltaOf<"text_delta" | "thinking_delta" | "toolcall_delta" | "image_end" | "toolcall_end"> | (DeltaOf<"text_start" | "thinking_start" | "toolcall_start"> & {
	block?: AssistantMessage["content"][number];
}) | (DeltaOf<"text_end"> & {
	block?: Omit<TextContent, "text">;
}) | (DeltaOf<"thinking_end"> & {
	block?: Omit<ThinkingContent, "thinking">;
}) | (DeltaOf<"start"> & {
	message: AssistantMessage;
}) | Extract<AssistantMessageEvent, {
	type: "done" | "error";
}>;
/** Opt-in projection (`messageUpdates: "delta"`): enough to rebuild the message client-side; terminal subtypes pass through. */
export type RpcDeltaMessageUpdateFrame = Omit<Extract<RpcMessageEventFrame, {
	type: "message_update";
}>, "message" | "assistantMessageEvent"> & {
	message: Pick<AgentMessage, "role">;
	assistantMessageEvent: RpcDeltaAssistantMessageEvent;
};
/** Session event as written to stdout by default: message lifecycle events carry a `messageId`. */
export type RpcAgentSessionEventFrame = Exclude<AgentSessionEvent, {
	type: RpcMessageEventType;
}> | RpcMessageEventFrame;
/** Every session event shape RPC mode can write, including the opt-in `messageUpdates: "delta"` projection. */
export type RpcProjectedSessionEventFrame = RpcAgentSessionEventFrame | RpcDeltaMessageUpdateFrame;
export type RpcSessionEventFrame = RpcAgentSessionEventFrame | RpcSubagentFrame;
/** Positional presentation metadata for an RPC select option. */
export interface RpcExtensionUISelectOptionDetail {
	description?: string;
}
/** One question of an RPC `ask` dialog. Options never include "Other"; hosts always offer free text. */
export interface RpcAskDialogQuestion {
	id: string;
	question: string;
	header?: string;
	options: Array<{
		label: string;
		description?: string;
		preview?: string;
	}>;
	multi?: boolean;
	/** Index of the recommended option. */
	recommended?: number;
}
/** A form block as hosts receive it: pictures travel as their alt text. */
export type RpcFormBlock = Exclude<FormBlock, {
	kind: "image" | "svg";
}>;
/** One titled part of an RPC `form` dialog. */
export interface RpcFormSection {
	title: FormSpan[];
	blocks: RpcFormBlock[];
}
/** Emitted when an extension needs user input */
export type RpcExtensionUIRequest = {
	type: "extension_ui_request";
	id: string;
	method: "select";
	title: string;
	options: string[];
	optionDetails?: RpcExtensionUISelectOptionDetail[];
	timeout?: number;
} | {
	type: "extension_ui_request";
	id: string;
	method: "confirm";
	title: string;
	message: string;
	timeout?: number;
} | {
	type: "extension_ui_request";
	id: string;
	method: "askDialog";
	questions: ExtensionAskDialogQuestion[];
	timeout?: number;
} | {
	type: "extension_ui_request";
	id: string;
	method: "input";
	title: string;
	placeholder?: string;
	timeout?: number;
	/**
	 * Set when the prompt asks for a pasted OAuth authorization code: the id
	 * of the `open_url` request it belongs to, so hosts can show the field
	 * inside that dialog. omp closes the prompt (`cancel` frame) when the
	 * browser callback signs in first.
	 */
	authCodeFor?: string;
	/**
	 * Set on any other login prompt asked after an authorization URL, such as
	 * an API key: the id of that `open_url` request. The login waits for this
	 * answer, so a host must show the prompt while the `open_url` is still up.
	 */
	loginPromptFor?: string;
	/** Set on a login prompt whose answer may be left blank. */
	allowEmpty?: boolean;
	secret?: boolean;
} | {
	type: "extension_ui_request";
	id: string;
	method: "editor";
	title: string;
	prefill?: string;
	promptStyle?: boolean;
}
/** Emitted only after the host opts in with `set_ask_dialog`. */
 | {
	type: "extension_ui_request";
	id: string;
	method: "ask";
	questions: RpcAskDialogQuestion[];
	timeout?: number;
}
/** The whole form of one `ask` tool call. Emitted only after `set_ask_dialog` with `form: true`. */
 | {
	type: "extension_ui_request";
	id: string;
	method: "form";
	sections: RpcFormSection[];
	timeout?: number;
} | {
	type: "extension_ui_request";
	id: string;
	method: "cancel";
	targetId: string;
} | {
	type: "extension_ui_request";
	id: string;
	method: "notify";
	message: string;
	notifyType?: "info" | "warning" | "error";
} | {
	type: "extension_ui_request";
	id: string;
	method: "setStatus";
	statusKey: string;
	statusText: string | undefined;
} | {
	type: "extension_ui_request";
	id: string;
	method: "setWidget";
	widgetKey: string;
	widgetLines: string[] | undefined;
	widgetPlacement?: "aboveEditor" | "belowEditor";
} | {
	type: "extension_ui_request";
	id: string;
	method: "setTitle";
	title: string;
} | {
	type: "extension_ui_request";
	id: string;
	method: "set_editor_text";
	text: string;
	images?: ImageContent[];
	prepend?: boolean;
} | {
	type: "extension_ui_request";
	id: string;
	/**
	 * Answering with `cancelled: true` cancels the `login` or `mcp_reauth`
	 * that emitted it. omp closes a dialog the host has not answered
	 * (`cancel` frame) when that flow ends.
	 */
	method: "open_url";
	url: string;
	/**
	 * Short loopback URL that 302-redirects to {@link url}. When present,
	 * hosts SHOULD surface it as the copy target so terminal viewport
	 * truncation cannot corrupt OAuth query parameters on the full URL.
	 */
	launchUrl?: string;
	instructions?: string;
};
export interface RpcHostToolDefinition {
	name: string;
	label?: string;
	description: string;
	parameters: Record<string, unknown>;
	hidden?: boolean;
	/** How this host tool is presented when enabled; omission normalizes to `"discoverable"` at the adapter boundary. */
	loadMode?: ToolLoadMode;
	/** Whether this host tool can read `skill://` instruction content. */
	readsSkillUris?: boolean;
}
/** Emitted by the RPC server when it needs the host to execute a registered tool. */
export interface RpcHostToolCallRequest {
	type: "host_tool_call";
	id: string;
	toolCallId: string;
	toolName: string;
	arguments: Record<string, unknown>;
}
/** Emitted by the RPC server when a pending host tool call should be aborted. */
export interface RpcHostToolCancelRequest {
	type: "host_tool_cancel";
	id: string;
	targetId: string;
}
/** Sent by the host to stream partial tool updates back to the RPC server. */
export interface RpcHostToolUpdate {
	type: "host_tool_update";
	id: string;
	partialResult: AgentToolResult<unknown>;
}
/** Sent by the host to complete a pending tool call. */
export interface RpcHostToolResult {
	type: "host_tool_result";
	id: string;
	result: AgentToolResult<unknown>;
	isError?: boolean;
}
export interface RpcHostUriSchemeDefinition {
	/** URL scheme without trailing `://` (e.g. `db`, `notion`). */
	scheme: string;
	/** Optional human-readable description for logs/diagnostics. */
	description?: string;
	/** When true, the write tool is allowed to dispatch writes to this scheme. */
	writable?: boolean;
	/** When true, downstream callers suppress hashline anchors for resolved content. */
	immutable?: boolean;
}
export type RpcHostUriOperation = "read" | "write";
/** Emitted by the RPC server when it needs the host to satisfy a URI operation. */
export interface RpcHostUriRequest {
	type: "host_uri_request";
	id: string;
	operation: RpcHostUriOperation;
	url: string;
	/** Present for write operations. */
	content?: string;
}
/** Emitted by the RPC server when a pending URI request should be aborted. */
export interface RpcHostUriCancelRequest {
	type: "host_uri_cancel";
	id: string;
	targetId: string;
}
/** Sent by the host to complete a pending URI request. */
export interface RpcHostUriResult {
	type: "host_uri_result";
	id: string;
	/**
	 * Required for successful `read` results. Ignored for `write` success.
	 * Set on errors when a textual explanation accompanies `isError`.
	 */
	content?: string;
	/** Defaults to `text/plain` when omitted. */
	contentType?: "text/markdown" | "application/json" | "text/plain";
	/** Optional resolution notes propagated to the read tool. */
	notes?: string[];
	/** Overrides the scheme-level `immutable` flag for this single resolution. */
	immutable?: boolean;
	/** When true, surface the result content as an error to the caller. */
	isError?: boolean;
	/** Optional error message; preferred over `content` for error surfacing. */
	error?: string;
}
/** Response to an extension UI request */
export type RpcExtensionUIResponse = {
	type: "extension_ui_response";
	id: string;
	value: string;
} | {
	type: "extension_ui_response";
	id: string;
	confirmed: boolean;
} | {
	type: "extension_ui_response";
	id: string;
	askDialog: ExtensionAskDialogResult;
} | {
	type: "extension_ui_response";
	id: string;
	cancelled: true;
	timedOut?: boolean;
}
/** Answers to a `form` request by control name: a string, or a string array for a multi-choice field. */
 | {
	type: "extension_ui_response";
	id: string;
	formData: Record<string, unknown>;
}
/** Answers to an `ask` request, one per question in request order. */
 | {
	type: "extension_ui_response";
	id: string;
	answers: Array<{
		id: string;
		selectedOptions: string[];
		customInput?: string;
	}>;
};
export type RpcCommandType = RpcCommand["type"];

export {};
