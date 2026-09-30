//go:build !linux

package main

// The kernel log only exists on Linux.
// 커널 로그는 리눅스에만 있다.

func mirrorLine(string) {}
