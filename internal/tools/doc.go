// Package tools 提供视频工厂 workflow 可复用的确定性工具。
//
// 本包只放稳定的小能力：商品输入规整、平台模板选择、素材缺口提示、
// 字幕切分和平台 QA。真实 tRPC-Agent-Go 接入后，这些函数会被注册为
// Tool，供 GraphAgent 节点调用；当前 MVP 直接由 deterministic workflow 调用。
package tools
