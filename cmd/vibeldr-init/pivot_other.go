//go:build !linux

package main

// pivotRoot only means anything on the machine the helper actually runs on.
// pivotRoot 는 헬퍼가 실제로 도는 머신에서만 의미가 있다.
func pivotRoot(newroot, init string) { logf("pivot: not supported on this platform") }
