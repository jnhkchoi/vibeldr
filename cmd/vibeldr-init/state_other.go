//go:build !linux

package main

// writeStateLog only exists so this program still compiles on a development
// machine; the commands it runs are DSM's.
//
// writeStateLog - 개발 머신에서도 컴파일되게 하려고 둔 껍데기다. 돌리는 명령은
// DSM 의 것이다.
func writeStateLog() {}
