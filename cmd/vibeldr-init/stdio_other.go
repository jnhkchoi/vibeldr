//go:build !linux

package main

// ensureStdio does nothing off Linux. The problem it guards against is the
// kernel starting /init with no console, which only happens there.
//
// ensureStdio - 리눅스가 아니면 아무것도 하지 않는다. 막으려는 상황이
// 커널이 콘솔 없이 /init 을 시작하는 것이고, 그건 리눅스에서만 일어난다.
func ensureStdio() {}
