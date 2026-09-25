//go:build !linux

package main

import "errors"

// These only exist so this program still compiles on a development machine.
// It is never built for anything but the ramdisk.
//
// 개발 머신에서도 이 프로그램이 컴파일되게 하려고 둔 껍데기일 뿐이다.
// 램디스크 말고 다른 것을 위해 빌드되는 일은 없다.

func spawnStage2() error {
	return errors.New("a rescue console only makes sense inside the ramdisk")
}

func runDaemon() error {
	return errors.New("a rescue console only makes sense inside the ramdisk")
}
