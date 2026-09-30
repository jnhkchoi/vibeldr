//go:build !linux

package main

import "errors"

// runPowerButton only exists so this program still compiles on a development
// machine; the listener needs Linux netlink.
//
// runPowerButton - 개발 머신에서도 컴파일되게 하려고 둔 껍데기다. 리스너는
// 리눅스 netlink 가 있어야 한다.
func runPowerButton() error {
	return errors.New("the power button listener only runs on Linux")
}
