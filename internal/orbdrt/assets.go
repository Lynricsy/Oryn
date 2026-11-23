package orbdrt

import "embed"

// orbExtension 是写到 $HOME/.oryn/core/extensions/oryn-orb.ts 的 omp 扩展：
// 在 before_agent_start 向系统提示词追加「Oryn orb 环境」说明与最近 setup 现状。
//
//go:embed assets/oryn-orb.ts
var orbExtension []byte

// setupSkill 是写到 $HOME/.agents/skills/oryn-setup/SKILL.md 的技能。
//
//go:embed assets/oryn-setup/SKILL.md
var setupSkill []byte

// librarySkill 是写到 $HOME/.agents/skills/oryn-library/SKILL.md 的技能（M13：`oryn library put`）。
//
//go:embed assets/oryn-library/SKILL.md
var librarySkill []byte

// boxesSkill 是写到 $HOME/.agents/skills/oryn-boxes/SKILL.md 的技能（M23：`oryn boxes …`；runner 不发）。
//
//go:embed assets/oryn-boxes/SKILL.md
var boxesSkill []byte

// runnerLibrarySkill 是 runner 写到 $HOME/.oryn/omp/agent/skills/oryn-library/SKILL.md 的技能（M21：存入方式是
// 扩展的 oryn_library_put 工具，runner 上没有 `oryn` CLI）。
//
//go:embed assets/oryn-library-runner/SKILL.md
var runnerLibrarySkill []byte

// openboxRC 是写到 $HOME/.oryn/desktop/openbox-rc.xml 的窗口管理器配置（M8，仅桌面启用时）。
//
//go:embed assets/openbox-rc.xml
var openboxRC []byte

// openboxTheme 是写到 $HOME/.themes/Oryn/openbox-3/ 的 openbox 主题（themerc 与按钮图形；openbox-rc.xml 按名字 Oryn 引用）。
//
//go:embed assets/openbox-theme
var openboxTheme embed.FS
