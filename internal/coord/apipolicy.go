package coord

import (
	"net/url"
	"time"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

// opClass 是全局助手对一个 REST 操作的权限级别。
type opClass uint8

const (
	opDeny    opClass = iota // 零值：表里没有的操作一律拒绝
	opRead                   // 直接执行；结果加不可信包裹；不限速、不审计
	opWrite                  // 直接执行；计 mutate 限速；写审计 api_call
	opConfirm                // 挂确认卡；计 mutate 限速；批准后执行并写审计
)

func (c opClass) String() string {
	switch c {
	case opRead:
		return "read"
	case opWrite:
		return "write"
	case opConfirm:
		return "confirm"
	default:
		return "deny"
	}
}

// opGuard 是操作目标的额外校验。
type opGuard uint8

const (
	guardNone   opGuard = iota
	guardThread         // path 参数 id 必须是工作线程：resolveActTarget(ctx, c, id, false)
	guardOrb            // path 参数 id 不能是协调宿主、助手的电脑、box、预热池 orb：orbTarget(ctx, id)
)

// opPolicy 是一个 REST 操作对全局助手的策略。
type opPolicy struct {
	Class       opClass
	Domain      string // 技能里的域；内部接口为空串
	Guard       opGuard
	Share       bool                                             // 对外公开类：另计 share 限速
	ConfirmWhen func(body map[string]any, query url.Values) bool // 非 nil 且返回真时，opWrite 改走确认卡
	ConfirmNote string                                           // ConfirmWhen 的文字说明，进操作文档
	Check       func(path map[string]string) error               // 额外的参数规则
	Note        string                                           // 追加到确认卡正文与操作文档
	Deny        string                                           // opDeny 的原因（英文一句，含替代做法）；空串 = 内部接口，不进文档
	Timeout     time.Duration                                    // 0 = apiCallTimeout
}

// 拒绝原因（文案固定，进技能文档与工具错误）。
const (
	denyHuman      = "needs the owner in person; ask the owner to do it in Oryn"
	denyCredential = "its result is a credential that must not pass through a model; ask the owner to do it in Settings"
	denyClient     = "belongs to one browser or app session"
	denySSHFiles   = "work on SSH hosts through a thread: threads have the SSH tools and their approvals"
)

func denyNative(tool string) string { return "use " + tool + " instead" }
func denyBinary(alt string) string  { return "binary or streamed content; " + alt }

// apiDomains 是技能的域（顺序即索引表顺序）及其说明。
var apiDomains = []struct{ Name, Desc string }{
	{"threads", "Rename, label, pin, snooze, move, archive, fork, wake and delete threads; stop a turn; switch model; attachments"},
	{"thread-files", "Files and git state in a thread's orb or runner directory"},
	{"projects", "Projects and their settings, repository mirror, hosted repositories and webhooks, snapshots, agent configuration files, project secrets"},
	{"spaces", "Spaces and their progress"},
	{"assistant", "Your own settings, identity, computer, main-conversation notes and library files"},
	{"orbs", "Orbs (pause, resume, delete, metrics, setup runs, services), providers and runners"},
	{"desktop", "Browser profiles and the desktop of an orb"},
	{"boxes", "Boxes and their apps, releases, schedules and secrets"},
	{"shares", "Public share links, published files and public Portal previews"},
	{"artifacts", "Artifact versions, comments, page data, export and import, trash, publication and settings"},
	{"schedules", "Inspect schedules and their runs, preview and trigger them"},
	{"github", "GitHub integration status, identities, trigger rules, tasks and PR links"},
	{"ssh", "SSH hosts and keys"},
	{"memory", "Engineering memory: settings, documents, search, mental models"},
	{"owner-memory", "Your memory about the owner: topics, facts, sources, exclusions"},
	{"notifications", "Notifications, notification channels and push subscriptions"},
	{"settings", "Identities and avatars, git identity, composer and selection actions, global agent configuration, secrets vault, devices"},
	{"insights", "Search, usage and cost statistics, budget, the automation log, system status"},
}

// apiPolicy 给规格里的每个 REST 操作定级（TestAPIPolicyComplete 强制覆盖全部操作）。
//
// 定级原则：GET → read；会销毁无法自动重建的数据、让未登录者能访问、涉及凭据或安全配置、改变助手自身约束的 → confirm；
// 其余写操作 → write；需要主人本人到场、是「主人把关」的闸门、结果是一次性凭据、二进制 / 流式、或有语义更完整的原生操作的 → deny。
var apiPolicy = buildAPIPolicy()

func buildAPIPolicy() map[string]opPolicy {
	m := map[string]opPolicy{}
	set := func(domain string, class opClass, p opPolicy, ops ...string) {
		for _, op := range ops {
			if _, dup := m[op]; dup {
				panic("coord: 策略重复: " + op)
			}
			p.Class, p.Domain = class, domain
			m[op] = p
		}
	}
	none := opPolicy{}
	thread := opPolicy{Guard: guardThread}
	orb := opPolicy{Guard: guardOrb}
	share := opPolicy{Share: true}
	deny := func(reason string) opPolicy { return opPolicy{Deny: reason} }

	// threads
	set("threads", opRead, none, "listThreads", "getThread", "listThreadEntries", "listThreadAttachments")
	set("threads", opWrite, thread, "updateThread", "wakeThread", "removeThreadWorktree")
	set("threads", opWrite, opPolicy{Guard: guardThread, Timeout: 5 * time.Minute}, "forkThread")
	set("threads", opConfirm, thread, "deleteThread")
	set("threads", opConfirm, none, "deleteAttachment")
	set("threads", opDeny, deny(denyNative("create_thread")), "createThread")
	set("threads", opDeny, deny(denyBinary("pass library file ids in create_thread's files, or use put_thread_file")), "uploadAttachment")
	set("threads", opDeny, deny(denyBinary("use read_thread_file on the copy in the thread's orb")), "getAttachmentContent")

	// thread-files
	set("thread-files", opRead, none, "getOrbFsRoots", "statOrbFs", "listOrbGitCommits")
	set("thread-files", opWrite, orb, "mkdirOrbFs", "renameOrbFs", "stageOrbGit", "unstageOrbGit")
	set("thread-files", opConfirm, orb, "deleteOrbFs")
	set("thread-files", opDeny, deny(denyNative("list_thread_files")), "listOrbFs")
	set("thread-files", opDeny, deny(denyNative("read_thread_file")), "readOrbFile")
	set("thread-files", opDeny, deny(denyNative("write_thread_file or put_thread_file")), "writeOrbFile")
	set("thread-files", opDeny, deny(denyNative("get_thread_changes")), "getOrbGitStatus", "getOrbGitDiff")

	// projects
	set("projects", opRead, none, "listProjects", "getProject", "getProjectRepo", "listProjectRepoRefs", "listProjectRepoCommits",
		"getProjectRepoCommit", "getProjectRepoCommitDiff", "getProjectRepoTree", "getProjectRepoBlob", "listProjectSnapshots",
		"listProjectAgentConfigFiles", "getProjectAgentConfigFile", "listProjectSecrets")
	set("projects", opWrite, none, "createProject", "syncProjectRepo", "createProjectHostedRepo", "deleteProjectSnapshots",
		"putProjectAgentConfigFile", "deleteProjectAgentConfigFile")
	set("projects", opWrite, opPolicy{
		ConfirmWhen: func(body map[string]any, _ url.Values) bool { _, ok := body["githubGates"]; return ok },
		ConfirmNote: "changing githubGates needs confirmation",
	}, "updateProject")
	set("projects", opConfirm, opPolicy{Note: "If the project has an Oryn-hosted repository, that repository is the only copy and is deleted permanently."}, "deleteProject")
	set("projects", opConfirm, none, "putProjectWebhook", "deleteProjectWebhook", "putProjectSecret", "deleteProjectSecret")
	set("projects", opDeny, deny(denyBinary("write files one by one with putProjectAgentConfigFile")), "importProjectAgentConfig")

	// spaces
	set("spaces", opRead, none, "listSpaces", "getSpace", "getSpaceProgress")
	set("spaces", opWrite, none, "createSpace", "updateSpace")
	set("spaces", opConfirm, none, "deleteSpace")

	// assistant
	set("assistant", opRead, none, "listAssistantMcpServers", "getAssistantMain", "getAssistantNotes", "getAssistantComputer",
		"listAssistantFiles", "getAssistantSettings", "getAssistantIdentity", "getResearchSettings", "listResearchTopics",
		"getResearchTopic", "listResearchRuns")
	set("assistant", opWrite, none, "pauseAssistantComputer", "putAssistantIdentity", "createResearchTopic",
		"updateResearchTopic", "deleteResearchTopic", "runResearchTopic")
	set("assistant", opConfirm, none, "resetAssistantComputer", "deleteAssistantFile", "putAssistantSettings", "putResearchSettings")
	set("assistant", opDeny, deny("only the owner starts assistant conversations"), "createAssistantConversation")
	set("assistant", opDeny, deny(denyNative("update_main_notes")), "putAssistantNotes")
	set("assistant", opDeny, deny(denyNative("computer_status")), "wakeAssistantComputer")
	set("assistant", opDeny, deny(denyNative("save_file")), "uploadAssistantFile")
	set("assistant", opDeny, deny(denyNative("share_file")), "patchAssistantFile")
	set("assistant", opDeny, deny(denyBinary("read the file at its ~/library path (list_files shows it)")), "getAssistantFileContent")
	set("assistant", opDeny, deny("only the owner inspects MCP servers"), "listAssistantMcpServerTools")

	// orbs
	set("orbs", opRead, none, "listOrbs", "getOrb", "getOrbMetrics", "listOrbSetupRuns", "getOrbSetupRunLog", "listOrbServices",
		"getOrbServiceLog", "getOrbTimeline", "listProviders", "listRunners")
	set("orbs", opWrite, orb, "pauseOrb", "resumeOrb", "stopOrbService")
	set("orbs", opWrite, opPolicy{Guard: guardOrb, Timeout: 90 * time.Second}, "startOrbService", "restartOrbService")
	set("orbs", opConfirm, orb, "deleteOrb")
	set("orbs", opConfirm, none, "deleteProvider", "deleteRunner")
	set("orbs", opDeny, deny(denyCredential), "createProvider", "createRunner")

	// desktop
	set("desktop", opRead, none, "listBrowserProfiles")
	set("desktop", opWrite, none, "createBrowserProfile", "updateBrowserProfile")
	set("desktop", opWrite, orb, "setOrbDesktopProfile", "saveOrbDesktopProfile", "startOrbBrowser", "stopOrbBrowser",
		"launchOrbDesktopApp", "operateOrbDesktopWindow")
	set("desktop", opConfirm, none, "deleteBrowserProfile")
	set("desktop", opDeny, deny(denyHuman), "setOrbDesktopControl")
	set("desktop", opDeny, deny(denyBinary("an image")), "getOrbDesktopAppIcon")

	// boxes
	set("boxes", opRead, none, "listBoxes", "listAllBoxActivity", "getBox", "listBoxActivity", "getBoxUsage", "listBoxReleases", "listBoxSecrets")
	set("boxes", opWrite, none, "createBox", "updateBox", "startBox", "stopBox", "restartBoxApp", "rollbackBoxApp", "cancelBoxDeploy")
	set("boxes", opWrite, opPolicy{
		Share:       true,
		ConfirmWhen: func(body map[string]any, _ url.Values) bool { return body["public"] == true },
		ConfirmNote: "public: true needs confirmation",
	}, "updateBoxApp")
	set("boxes", opConfirm, none, "deleteBox", "undeployBoxApp", "putBoxSecret", "deleteBoxSecret")
	set("boxes", opDeny, deny(denyBinary("streamed; start a thread on the box (create_thread with box) to run commands")), "execBox")

	// shares（全部另计 share 限速）
	set("shares", opRead, share, "listThreadShares", "listShares")
	set("shares", opWrite, share, "revokeShare", "revokePublishedFile")
	set("shares", opWrite, opPolicy{Share: true, Guard: guardOrb}, "endOrbPortalShare")
	set("shares", opConfirm, opPolicy{Share: true, Guard: guardThread}, "createThreadShare")
	set("shares", opConfirm, opPolicy{Share: true, Guard: guardOrb}, "publishOrbFile", "setOrbPortalShare")
	set("shares", opConfirm, share, "publishAttachment")

	// artifacts
	set("artifacts", opRead, none, "listArtifacts", "getArtifact", "listArtifactVersions", "getArtifactCapabilities",
		"listArtifactComments", "getArtifactCanvas", "listArtifactConsents", "getArtifactPublication", "listArtifactPublications", "getArtifactPrefs")
	set("artifacts", opWrite, none, "patchArtifact", "undeleteArtifact", "revokeArtifactConsents", "putArtifactPrefs")
	set("artifacts", opWrite, share, "unpublishArtifact", "rotateArtifactPublicationCode")
	set("artifacts", opWrite, opPolicy{
		Share: true,
		ConfirmWhen: func(body map[string]any, _ url.Values) bool {
			return body["access"] == "code" || body["access"] == "public"
		},
		ConfirmNote: "access code or public needs confirmation",
	}, "publishArtifact")
	set("artifacts", opConfirm, opPolicy{Note: "Use delete_artifact to move an artifact to the trash; this operation is for permanent=true."}, "deleteArtifact")
	set("artifacts", opDeny, deny(denyNative("restore_artifact_version")), "restoreArtifactVersion")
	set("artifacts", opDeny, deny(denyNative("reply_artifact_comment")), "replyArtifactComment")
	set("artifacts", opDeny, deny(denyNative("resolve_artifact_comment")), "patchArtifactComment")
	set("artifacts", opDeny, deny(denyNative("update_artifact")), "editArtifactElement", "applyArtifactCanvasOps", "setArtifactParams")
	set("artifacts", opDeny, deny(denyNative("import_artifact")), "importArtifact")
	set("artifacts", opDeny, deny(denyNative("export_artifact")), "exportArtifact")
	set("artifacts", opDeny, deny(denyNative("read_artifact")), "getArtifactFile")
	set("artifacts", opDeny, deny(denyNative("screenshot_artifact")), "getArtifactThumbnail", "renderArtifactPng")
	set("artifacts", opDeny, deny(denyHuman), "createArtifactComment", "answerArtifactConsent")
	set("artifacts", opDeny, deny(denyClient), "markArtifactOpened", "markArtifactCommentsSent", "openArtifactFrame",
		"closeArtifactFrame", "openArtifactLink", "grantArtifactPublication")

	// schedules：REST 路径会把定时记成主人定义的，绕过 agent 定时的间隔、overlap、keepLast 与数量上限
	set("schedules", opRead, none, "listSchedules", "getSchedule", "listScheduleRuns", "getThreadSchedule", "previewSchedule")
	set("schedules", opWrite, none, "runSchedule")
	set("schedules", opDeny, deny(denyNative("set_schedule")), "createSchedule", "putSchedule", "deleteSchedule", "pauseSchedule",
		"resumeSchedule", "putThreadSchedule", "deleteThreadSchedule")

	// github
	set("github", opRead, none, "getGitHubStatus", "listGitHubRepos", "listGitHubHooks", "listGitHubAudit", "listGitHubTasks",
		"listGitHubPresets", "listGitHubTriggers", "listGitHubRefs")
	set("github", opWrite, none, "cleanupGitHubTask", "triageGitHubIssue", "replayGitHubDelivery", "createGitHubTrigger", "updateGitHubTrigger")
	set("github", opWrite, thread, "putThreadPull")
	set("github", opConfirm, none, "deleteGitHubTrigger", "deleteGitHubIdentity", "putGitHubSettings", "setGitHubOAuthApp", "deleteGitHubOAuthApp")
	set("github", opConfirm, thread, "deleteThreadPull")
	set("github", opDeny, deny(denyHuman), "authorizeGitHubIdentity")
	set("github", opDeny, deny(denyClient), "attachGitHubRef")

	// ssh
	set("ssh", opRead, none, "listSshHosts", "getSshHost", "listSshKeys", "listSshAudit", "listThreadSshHosts")
	set("ssh", opWrite, none, "testSshHost", "renameSshKey")
	set("ssh", opConfirm, none, "createSshHost", "updateSshHost", "deleteSshHost", "resetSshHostKey", "createSshKey", "deleteSshKey")
	set("ssh", opDeny, deny(denySSHFiles), "getSshFsRoots", "listSshFs", "statSshFs", "readSshFile", "writeSshFile",
		"mkdirSshFs", "renameSshFs", "deleteSshFs")

	// memory
	set("memory", opRead, none, "getMemorySettings", "getMemoryStatus", "listMemoryProjects", "getMemoryModels", "getMemoryEndpoints",
		"getMemoryUsage", "listMemoryDocuments", "searchMemory", "listMemoryCandidates", "listMemoryMentalModels",
		"listMemoryPendingDeletes", "getMemoryExport")
	set("memory", opWrite, none, "putMemoryProject", "putMemoryModels", "refreshMemoryMentalModel", "createMemoryExport")
	set("memory", opConfirm, none, "putMemorySettings", "putMemoryEndpoint", "deleteMemoryEndpoint", "deleteMemoryDocument", "forgetMemoryThread")
	set("memory", opDeny, deny(denyHuman), "confirmMemoryModelsDeployed", "approveMemoryCandidate", "rejectMemoryCandidate",
		"createGlobalMemory", "updateGlobalMemory")
	set("memory", opDeny, deny(denyCredential), "createMemoryEnvFile")
	set("memory", opDeny, deny(denyBinary("the owner downloads it in Settings")), "downloadMemoryExport")

	// owner-memory
	set("owner-memory", opRead, none, "getOwnerMemorySettings", "getOwnerMemoryStatus", "listOwnerMemoryTopics", "getOwnerMemoryBrief",
		"listOwnerMemoryFacts", "listOwnerMemorySources", "listOwnerMemoryExclusions", "listOwnerMemoryExplicit")
	set("owner-memory", opWrite, none, "refreshOwnerMemoryTopic", "curateOwnerMemoryTopics", "setOwnerMemoryFactState", "deleteOwnerMemoryExclusion")
	set("owner-memory", opConfirm, none, "putOwnerMemorySettings", "putOwnerMemoryDecideEndpoint", "deleteOwnerMemoryDecideEndpoint",
		"forgetOwnerMemorySource", "putOwnerMemoryExclusion", "deleteOwnerMemoryExplicit")
	set("owner-memory", opDeny, deny(denyNative("remember")), "createOwnerMemoryExplicit")
	set("owner-memory", opDeny, deny(denyHuman), "updateOwnerMemoryExplicit", "approveOwnerMemoryExplicit")

	// notifications
	set("notifications", opRead, none, "listNotifications", "listNotificationChannels", "getVapidKey", "listPushSubscriptions")
	set("notifications", opWrite, none, "markNotificationsRead", "testNotificationChannel")
	set("notifications", opConfirm, none, "createNotificationChannel", "updateNotificationChannel", "deleteNotificationChannel", "deletePushSubscription")
	set("notifications", opDeny, deny(denyHuman), "answerNotification")       // 它能作答助手自己的确认卡
	set("notifications", opDeny, deny(denyCredential), "linkTelegramChannel") // 绑定码泄露等于把确认卡交给别人
	set("notifications", opDeny, deny(denyClient), "createPushSubscription")

	// settings
	set("settings", opRead, none, "getGitIdentity", "getAgentIdentity", "listRoleIdentities", "listAgentAvatars", "getComposerPrefs",
		"getSelectionActions", "getModelCatalog", "getUserIdentity", "listAgentConfigFiles", "getAgentConfigApplyStatus", "listSecrets", "listDevices")
	set("settings", opRead, opPolicy{Check: func(path map[string]string) error {
		// 与 Home.Get 同一套规范化（agentcfg.CleanPath）：./mcp.json、x/../mcp.json 之类的等价路径同样拒绝。
		// 规范化失败的路径 Home.Get 也会以同一错误拒绝、读不到文件，交给处理器回原有错误码。
		if c, err := agentcfg.CleanPath(path["path"]); err == nil && c == agentcfg.MCPFile {
			return errf(CodeNotAvailable, "MCP credentials stay outside your sandbox; use listAssistantMcpServers")
		}
		return nil
	}}, "getAgentConfigFile")
	set("settings", opWrite, none, "putGitIdentity", "deleteGitIdentity", "putAgentIdentity", "putRoleIdentity", "putAgentAvatar",
		"putComposerPrefs", "putSelectionActions", "putUserIdentity")
	set("settings", opConfirm, none, "putAgentConfigFile", "deleteAgentConfigFile", "restartAgentConfigSessions", "putSecret", "deleteSecret", "revokeDevice")
	set("settings", opDeny, deny(denyBinary("write files one by one with putAgentConfigFile")), "importAgentConfig")
	set("settings", opDeny, deny(denyClient), "getMe")
	set("settings", opDeny, deny(denyHuman), "authSetupBegin", "authSetupFinish", "authLoginBegin", "authLoginFinish", "authRecovery",
		"authLogout", "createAppSession", "devicePairStart", "getDevicePairing", "devicePairApprove", "devicePairPoll")

	// insights
	set("insights", opRead, none, "getHealthz", "searchMessages", "getStatsMeta", "getUsageStats", "exportUsageRecords", "getToolStats",
		"getOrbStats", "getOutputStats", "getBudget", "getCoordinator", "listAutomationActions")
	set("insights", opWrite, none, "putBudget")

	// 内部接口：不是用户操作，不进文档
	set("", opDeny, none, "getBlob", "createWsTicket", "connectWs", "orbDesktopSocket", "sshHostTerminal", "connectPublicShareWs",
		"getPublicShare", "getPublicShareIdentity", "listPublicShareEntries", "getPublicShareBlob", "receiveGitHubWebhook",
		"finishGitHubOAuth", "providerHello", "providerClaimJob", "providerCompleteJob", "providerBye", "providerClaimFs",
		"providerReplyFs", "orbLink", "orbWhoami", "prepareOrbSync", "getOrbSyncBundle")
	return m
}

// AllowedAPIOps 返回助手可用（Class != opDeny）的 operationId（规格顺序），供 internal/api 的测试遍历。
func AllowedAPIOps() []string {
	var out []string
	for _, op := range apiOps() {
		if apiPolicy[op].Class != opDeny {
			out = append(out, op)
		}
	}
	return out
}

// APIEndpoint 返回操作的 HTTP 方法与路径模板（供 internal/api 的测试构造请求）。
func APIEndpoint(op string) (method, path string, ok bool) {
	rt, ok := apiRoutes()[op]
	return rt.Method, rt.Path, ok
}
