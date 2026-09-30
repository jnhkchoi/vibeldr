//go:build !linux

package main

import "errors"

// nextBootLoader only exists so this program still compiles on a development
// machine; it mounts the loader disk, which needs Linux.
//
// nextBootLoader - 개발 머신에서도 컴파일되게 하려고 둔 껍데기다. 로더 디스크를
// 붙여야 하므로 리눅스가 있어야 한다.
func nextBootLoader() error { return errors.New("needs Linux") }
