//go:build !windows

package main

// 非 Windows 平台没有系统代理开关。

import "errors"

func readSystemProxyEnable() (bool, error) {
	return false, errors.New("该平台不支持读取系统代理开关")
}

func setSystemProxyEnabled(_ bool) error {
	return errors.New("该平台不支持修改系统代理开关")
}

func refreshInternetSettings() {}
