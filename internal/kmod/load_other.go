//go:build !linux

package kmod

import "fmt"

// Load only exists so the package builds and its tests run on a development
// machine. Nothing but the ramdisk helper ever calls it.
//
// Load - 개발 머신에서도 이 패키지가 빌드되고 테스트가 돌게 하려고 둔
// 껍데기. 실제로 부르는 것은 램디스크 헬퍼뿐이다.
func Load(m Module) error {
	return fmt.Errorf("kmod: loading %s: kernel modules can only be loaded on Linux", m.Name)
}
