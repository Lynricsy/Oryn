package api

// area 聚合各领域处理器的依赖。每个领域在自己的 server_<领域>.go 中定义 <领域>Deps，
// 并在此处加一个指针字段（这是唯一需要多方编辑的位置，改动只加行）。
type area struct{}
