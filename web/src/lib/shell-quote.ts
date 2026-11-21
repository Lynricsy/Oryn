/** 给 POSIX shell 的单个参数加引号（M24：页头复制 `oryn clone <项目名>`）：安全字符原样，否则单引号包住。 */
const SAFE = /^[A-Za-z0-9._@%+=:,/-]+$/;

export function shellArg(s: string): string {
	if (SAFE.test(s)) return s;
	return `'${s.replaceAll("'", "'\\''")}'`;
}
