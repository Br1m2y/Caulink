package accel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// stateFile 是保存目录的持久化结构。
//
// 只存一个字段，看起来不值得单开一个文件 —— 但"下次启动还要重新选目录"
// 是内置下载器最烦人的体验之一，而多存一个字段的成本比让用户重选低得多。
// 一旦将来要记并发数、限速，也都有地方放。
type stateFile struct {
	Dir string `json:"dir"`
}

// NormalizeDir 把用户填的目录整理成绝对路径，并按需创建。
//
// 接受这些写法（都是从资源管理器/运行框里复制出来的常见形态）：
//
//	D:\Downloads\caulink
//	D:/Downloads/caulink
//	"%USERPROFILE%\Downloads\caulink"     ← 复制"属性"里的路径会带引号
//	~/Downloads/caulink
func NormalizeDir(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("保存目录不能为空")
	}

	s = expandPath(s)
	if !filepath.IsAbs(s) {
		return "", fmt.Errorf("请填绝对路径（例如 D:\\Downloads\\caulink），当前是 %q", raw)
	}
	s = filepath.Clean(s)

	// 提前建目录，而不是等到下载时才发现写不进去：
	// 用户在点"开始"的那一刻就该知道这个路径能不能用。
	if err := os.MkdirAll(s, 0o755); err != nil {
		return "", fmt.Errorf("无法创建目录 %s：%w", s, err)
	}
	st, err := os.Stat(s)
	if err != nil {
		return "", fmt.Errorf("无法访问目录 %s：%w", s, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s 是一个文件，不是目录", s)
	}
	return s, nil
}

// expandPath 展开 ~ 与 %VAR% 两种写法。
func expandPath(s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			s = home + s[1:]
		}
	}
	if strings.Contains(s, "%") {
		s = os.Expand(s, os.Getenv)
	}
	return s
}

// loadState 读取上次记住的目录。读不到/不可用就保持默认值，不报错：
// 用户换了机器、删了目录，都不该让程序起不来。
func (m *Manager) loadState() {
	if m.opts.StatePath == "" {
		return
	}
	b, err := os.ReadFile(m.opts.StatePath)
	if err != nil {
		return
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		m.opts.Logger.Warn("下载目录配置损坏，使用默认值", "path", m.opts.StatePath, "err", err)
		return
	}
	if strings.TrimSpace(st.Dir) == "" {
		return
	}
	dir, err := NormalizeDir(st.Dir)
	if err != nil {
		m.opts.Logger.Warn("记住的下载目录不可用，回退默认值", "dir", st.Dir, "err", err)
		return
	}
	m.dir = dir
}

func (m *Manager) saveState(dir string) {
	if m.opts.StatePath == "" {
		return
	}
	b, err := json.MarshalIndent(stateFile{Dir: dir}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.opts.StatePath), 0o700); err != nil {
		m.opts.Logger.Warn("创建下载目录配置失败", "err", err)
		return
	}
	if err := os.WriteFile(m.opts.StatePath, append(b, '\n'), 0o600); err != nil {
		m.opts.Logger.Warn("保存下载目录失败", "err", err)
	}
}

// SetDefaultDir 校验并记住一个新的保存目录，返回规范化后的路径。
//
// 目录会立刻创建：这样用户在界面上改路径的瞬间就知道它能不能用，
// 而不是等点完"开始加速"再看到一个失败。
func (m *Manager) SetDefaultDir(raw string) (string, error) {
	dir, err := NormalizeDir(raw)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.dir = dir
	m.mu.Unlock()
	m.saveState(dir)
	return dir, nil
}
