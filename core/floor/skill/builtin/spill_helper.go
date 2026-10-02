// spill_helper.go — 共用工具: 命令输出截断 + 大输出 spill 到 /var/tmp/.
//
// 适用于所有 builtin skill (probe / diag / capture) 的 Execute 函数.
// 当命令输出超过 MaxInlineBytes 时, 落盘到 /var/tmp/opskeeper-<tool>-<ts>.log,
// 响应只返回前 1KB 预览 + 落盘路径.
package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MaxInlineBytes 是工具响应里允许的最大 inline 字节数. 超过则 spill 到磁盘.
const MaxInlineBytes = 1 * 1024 * 1024

// SpillDir 是大输出落盘目录.
const SpillDir = "/var/tmp"

// SpillResult 是 truncateOrSpill 的返回值.
type SpillResult struct {
	Inline     string `json:"inline"`  // 截断后的 inline 内容 (≤ 1KB 预览)
	Spilled    bool   `json:"spilled"` // 是否 spill 到磁盘
	SpillPath  string `json:"spill_path,omitempty"`
	TotalBytes int    `json:"total_bytes"` // 原始输出字节数
}

// truncateOrSpill 把原始输出截断到 1MB, 超过则落盘到 /var/tmp/.
// 落盘失败时降级为 hard truncate + 错误信息, 不 panic.
func truncateOrSpill(toolName string, output []byte) SpillResult {
	res := SpillResult{TotalBytes: len(output)}
	if len(output) <= MaxInlineBytes {
		res.Inline = string(output)
		return res
	}
	ts := time.Now().Unix()
	// 确保目录存在后落盘. 容器里 /var/tmp 可能不可写, 退化到 os.TempDir.
	dir := SpillDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, fmt.Sprintf("opskeeper-%s-%d.log", toolName, ts))
	// 退化的判据必须是"写失败", 不能只是 MkdirAll 失败: 一个已存在但只读的
	// 目录会让 MkdirAll 成功、WriteFile 失败, 而那正是容器里 /var/tmp 的
	// 样子 (存在、0755、root 所有). 只在 mkdir 上退化等于这条降级路径从未
	// 生效过.
	if err := os.WriteFile(path, output, 0o644); err != nil {
		if tmp := os.TempDir(); tmp != dir {
			if alt := filepath.Join(tmp, filepath.Base(path)); os.WriteFile(alt, output, 0o644) == nil {
				path, err = alt, nil
			}
		}
		if err != nil {
			// 落盘失败: hard truncate + 错误信息
			preview := output
			if len(preview) > 1024 {
				preview = preview[:1024]
			}
			res.Inline = fmt.Sprintf("%s\n[truncated, spill failed: %v]", preview, err)
			return res
		}
	}
	res.Spilled = true
	res.SpillPath = path
	preview := output
	if len(preview) > 1024 {
		preview = preview[:1024]
	}
	res.Inline = fmt.Sprintf("[%d bytes -> %s]\n%s", len(output), path, preview)
	return res
}
