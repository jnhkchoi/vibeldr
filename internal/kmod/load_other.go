//go:build !linux

package kmod

import "fmt"

// Load only exists so the package builds and its tests run on a development
// machine. Only the programs that run on the booted machine call it.
//
// Load - 개발 머신에서도 이 패키지가 빌드되고 테스트가 돌게 하려고 둔
// 껍데기. 실제로 부르는 것은 부팅된 머신에서 도는 프로그램뿐이다.
func Load(m Module) error {
	return fmt.Errorf("kmod: loading %s: kernel modules can only be loaded on Linux", m.Name)
}

// LoadWith is Load with module parameters; it only means anything on Linux.
// LoadWith - 모듈 파라미터를 붙인 Load. 리눅스에서만 의미가 있다.
func LoadWith(m Module, params string) error { return Load(m) }
