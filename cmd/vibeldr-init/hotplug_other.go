//go:build !linux

package main

// runHotplug and installHotplug only exist so this program still compiles on a
// development machine; loading modules needs Linux.
//
// runHotplug, installHotplug - 개발 머신에서도 컴파일되게 하려고 둔 껍데기다.
// 모듈을 올리려면 리눅스가 있어야 한다.
func runHotplug(modalias, devpath string) error { return nil }

func installHotplug(root string) {}
