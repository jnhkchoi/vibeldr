//go:build !linux

package main

// logfile_other.go is the stub that keeps this package building off Linux.
// There is no loader partition to write to anywhere else.
//
// logfile_other.go - 리눅스가 아닌 곳에서 이 패키지가 빌드되게 하는 스텁.
// 다른 곳에는 쓸 로더 파티션이 없다.

func keepLine(string) {}
func flushLog(string) {}
