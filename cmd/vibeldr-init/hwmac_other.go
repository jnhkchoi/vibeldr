//go:build !linux

package main

// applyHardwareMACs only means anything on the machine this actually runs on.
// The empty stub is here so the package keeps compiling on a development
// machine.
//
// applyHardwareMACs 는 실제로 도는 머신에서만 의미가 있다. 개발 머신에서
// 이 패키지가 계속 컴파일되도록 빈 껍데기를 둔다.
func applyHardwareMACs() {}
