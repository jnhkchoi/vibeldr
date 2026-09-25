//go:build linux

package kmod

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// sysFinitModule is finit_module(2) on linux/amd64. It takes a file descriptor
// rather than a buffer, so the module never has to be read into memory here.
//
// sysFinitModule - linux/amd64 의 finit_module(2). 버퍼가 아니라 파일
// 디스크립터를 받으므로 모듈을 메모리로 읽어들일 필요가 없다.
const sysFinitModule = 313

// Load inserts a module into the running kernel.
//
// A module that is already present reports EEXIST, and a module whose hardware
// is absent reports ENODEV. Neither is a failure worth stopping for, so both
// are reported as nil.
//
// Load - 도는 커널에 모듈을 넣는다.
//
// 이미 올라가 있으면 EEXIST, 해당 하드웨어가 없으면 ENODEV 가 온다. 둘 다
// 멈출 만한 실패가 아니라서 nil 로 돌려준다.
func Load(m Module) error {
	f, err := os.Open(m.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	// finit_module wants a NUL-terminated parameter string; we pass none.
	// finit_module 은 NUL 로 끝나는 파라미터 문자열을 받는다. 빈 값을 준다.
	args, err := syscall.BytePtrFromString("")
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(sysFinitModule, f.Fd(), uintptr(unsafe.Pointer(args)), 0)
	switch errno {
	case 0, syscall.EEXIST, syscall.ENODEV:
		return nil
	default:
		return fmt.Errorf("kmod: loading %s: %w", m.Name, errno)
	}
}

// sysInitModule is init_module(2): the older call, which takes the module
// image in memory rather than a file descriptor.
//
// sysInitModule - init_module(2). 파일 디스크립터가 아니라 메모리 위의
// 모듈 이미지를 받는 옛 호출이다.
const sysInitModule = 175

// LoadImage inserts a module the caller already holds in memory.
//
// The loader keeps its driver pack on a partition with no filesystem on it, so
// there is no file to open and no descriptor to hand to finit_module. This is
// the call that existed before there were descriptors to pass, and it is still
// there for exactly this case.
//
// Like Load, a module that is already present or whose hardware is absent is
// not treated as a failure.
//
// LoadImage - 호출자가 이미 메모리에 들고 있는 모듈을 넣는다.
//
// 로더는 드라이버 팩을 파일시스템 없는 파티션에 두므로 열 파일도, finit_module
// 에 넘길 디스크립터도 없다. 디스크립터를 넘기는 방식이 생기기 전부터 있던
// 호출이고, 정확히 이런 경우를 위해 아직 남아 있다.
//
// Load 와 마찬가지로 이미 올라가 있거나 해당 하드웨어가 없는 경우는 실패로
// 치지 않는다.
func LoadImage(image []byte) error {
	if len(image) == 0 {
		return fmt.Errorf("kmod: empty module image")
	}
	args, err := syscall.BytePtrFromString("")
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(sysInitModule,
		uintptr(unsafe.Pointer(&image[0])), uintptr(len(image)), uintptr(unsafe.Pointer(args)))
	switch errno {
	case 0, syscall.EEXIST, syscall.ENODEV:
		return nil
	}
	return errno
}
