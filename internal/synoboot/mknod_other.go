//go:build !linux

package synoboot

import "fmt"

// MknodBlock only exists so the package builds and its tests run on a
// development machine. Only the programs that run on the booted machine call
// it.
//
// MknodBlock - 개발 머신에서도 이 패키지가 빌드되고 테스트가 돌게 하려고
// 둔 껍데기. 실제로 부르는 것은 부팅된 머신에서 도는 프로그램뿐이다.
func MknodBlock(path string, major, minor uint32) error {
	return fmt.Errorf("synoboot: cannot create %s (%d:%d): device nodes are a Linux thing", path, major, minor)
}
