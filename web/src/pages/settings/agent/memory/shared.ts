/** 记忆页各卡片共用的小件：时间换算、文档地址、线程地址。 */

/** 部署文档「Hindsight 记忆服务」一节：部署、生成 .env、备份、升级（GitHub 按标题生成的锚点）。 */
export const MEMORY_DEPLOY_DOC_URL =
	"https://github.com/Lynricsy/Oryn/blob/main/docs/deploy.md#14-hindsight-记忆服务m16";

/** 同一节里的「换嵌入模型（reembed）」停机迁移流程。 */
export const MEMORY_REEMBED_DOC_URL =
	"https://github.com/Lynricsy/Oryn/blob/main/docs/deploy.md#换嵌入模型reembed";

/** 记忆接口的时间戳是 Unix 秒；`formatRelative` 等要毫秒。 */
export function secToMs(sec: number): number {
	return sec * 1000;
}

/** 线程的页面地址：助手对话在 `/assistant/<id>`，其余在 `/t/<id>`。 */
export function threadHref(threadId: string, kind: string | undefined): string {
	return kind === "assistant" ? `/assistant/${threadId}` : `/t/${threadId}`;
}
