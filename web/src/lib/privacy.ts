/** 隐私政策（托管在文档站）：简体中文界面给中文版，其余给英文版。 */
export function privacyPolicyUrl(language: string): string {
	return language.startsWith("zh")
		? "https://docs.oryn.best/privacy/"
		: "https://docs.oryn.best/privacy/en/";
}
