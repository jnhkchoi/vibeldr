//go:build !linux

package kmod

import "fmt"

// LoadImage only means anything on the machine the loader boots.
// LoadImage - 로더가 부팅시키는 머신에서만 의미가 있다.
func LoadImage(image []byte) error {
	return fmt.Errorf("kmod: loading a module is only possible on the target machine")
}

// LoadImageWith is LoadImage with module parameters; it only means anything on
// the machine the loader boots.
//
// LoadImageWith - 모듈 파라미터를 붙인 LoadImage. 로더가 부팅한 머신에서만
// 의미가 있다.
func LoadImageWith(image []byte, params string) error { return LoadImage(image) }
