//go:build !linux

package main

// detachDev only means anything on the machine the helper actually runs on.
// The rest of the program builds and is tested everywhere.
//
// detachDev 는 헬퍼가 실제로 도는 머신에서만 의미가 있다. 나머지 코드는
// 어디서나 빌드되고 테스트된다.
func detachDev() { logf("detach: not supported on this platform") }
