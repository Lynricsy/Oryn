package egress

// 托管仓库（M24 阶段 ③）：orb 所属项目的托管仓库由控制面在本地服务，不转发上游。

// HostedPusher 是托管仓库 git 请求的来源：OrbID 为空 = 主人本机（/git/ 入口）；否则是经出站代理的 orb，
// ThreadID 是它唯一的未归档工作线程（没有时为空）。
type HostedPusher struct{ OrbID, ThreadID string }
