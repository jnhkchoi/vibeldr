//go:build !linux

package main

// runDSMTasks only exists so this program still compiles on a development
// machine; it mounts the loader disk and runs DSM's sqlite3, which needs Linux.
//
// runDSMTasks - 개발 머신에서도 컴파일되게 하려고 둔 껍데기다. 로더 디스크를
// 붙이고 DSM 의 sqlite3 를 돌려야 하므로 리눅스가 있어야 한다.
func runDSMTasks(root string) {}
