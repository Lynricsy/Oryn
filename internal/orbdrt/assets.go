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

// designSkill 与 designNotice 是写到 $HOME/.agents/skills/oryn-design/{SKILL.md,NOTICE.md} 的设计画布技能（M29 C9）。
//
//go:embed assets/oryn-design/SKILL.md
var designSkill []byte

//go:embed assets/oryn-design/NOTICE.md
var designNotice []byte

// runnerDesignSkill 是 runner 写到 $HOME/.oryn/omp/agent/skills/oryn-design/SKILL.md 的版本（M29 C9）：runner 没有
// 设计画布的工作副本，去掉画布契约与自动封存两节，换成「用 HTML 画框做设计稿」。NOTICE.md 与 orb 版共用。
//
//go:embed assets/oryn-design/SKILL-runner.md
var runnerDesignSkill []byte

// officeSkill 与 runnerOfficeSkill 是 Office 文件技能 oryn-office（M31）：orb 写到 $HOME/.agents/skills/oryn-office/，
// runner 写到 $HOME/.oryn/omp/agent/skills/oryn-office/（SKILL-runner.md 作 SKILL.md，开头先检查本机工具）。
// scripts/ 下的脚本两边共用，以 0644 落盘，技能里一律写成 bash …/render.sh、python …/recalc.py 这样经解释器调用。
//
//go:embed assets/oryn-office/SKILL.md
var officeSkill []byte

//go:embed assets/oryn-office/SKILL-runner.md
var runnerOfficeSkill []byte

//go:embed assets/oryn-office/scripts/render.sh
var officeRenderScript []byte

//go:embed assets/oryn-office/scripts/recalc.py
var officeRecalcScript []byte

//go:embed assets/oryn-office/scripts/unpack.py
var officeUnpackScript []byte

//go:embed assets/oryn-office/scripts/pack.py
var officePackScript []byte

// openboxRC 是写到 $HOME/.oryn/desktop/openbox-rc.xml 的窗口管理器配置（M8，仅桌面启用时）。
//
//go:embed assets/openbox-rc.xml
var openboxRC []byte

// openboxTheme 是写到 $HOME/.themes/Oryn/openbox-3/ 的 openbox 主题（themerc 与按钮图形；openbox-rc.xml 按名字 Oryn 引用）。
//
//go:embed assets/openbox-theme
var openboxTheme embed.FS
