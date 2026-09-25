//go:build !linux

package synoboot

import "fmt"

// mknodBlock only exists so the package builds and its tests run on a
// development machine. Nothing but the ramdisk helper ever calls it.
//
// mknodBlock - 개발 머신에서도 이 패키지가 빌드되고 테스트가 돌게 하려고
// 둔 껍데기. 실제로 부르는 것은 램디스크 헬퍼뿐이다.
func mknodBlock(path string, major, minor uint32) error {
	return fmt.Errorf("synoboot: cannot create %s (%d:%d): device nodes are a Linux thing", path, major, minor)
}
