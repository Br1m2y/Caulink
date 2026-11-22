//go:build !windows

package main

// 非 Windows 平台没有系统代理注册表，按住功能不可用。

import "errors"

func readSystemProxy() (string, bool, error) {
	return "", false, errors.New("该平台不支持读取系统代理")
}

// setSystemProxy 在非 Windows 平台上不可用（Windows 实现在 steam_windows.go）。
func setSystemProxy(_ string) error {
	return errors.New("该平台不支持修改系统代理")
}
