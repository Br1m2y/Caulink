//go:build !windows

package main

import "os/exec"

// configureHidden 在非 Windows 平台上无需特殊处理。
func configureHidden(_ *exec.Cmd) {}
