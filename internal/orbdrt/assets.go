package orbdrt

import _ "embed"

// orbExtension 是写到 $HOME/.oryn/core/extensions/oryn-orb.ts 的 omp 扩展：
// 在 before_agent_start 向系统提示词追加「Oryn orb 环境」说明与最近 setup 现状。
//
//go:embed assets/oryn-orb.ts
var orbExtension []byte

// setupSkill 是写到 $HOME/.agents/skills/oryn-setup/SKILL.md 的技能。
//
//go:embed assets/oryn-setup/SKILL.md
var setupSkill []byte
