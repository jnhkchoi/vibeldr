// Package initbin carries the Linux helper that runs inside the DSM ramdisk,
// embedded into whatever tool needs to ship it.
//
// The helper is a separate program for a separate machine: no libc, built for
// linux/amd64, while the tool embedding it may be running on Windows. Building
// the helper cannot be part of building this package, so the build script
// compiles it into this directory first and this package only checks that what
// landed here is really an ELF binary.
//
// Package initbin - DSM 램디스크 안에서 도는 Linux 헬퍼를 embed 로 실어 나름.
//
// 헬퍼는 별개 머신용 별개 프로그램: no libc + linux/amd64 로 컴파일되고,
// embed 하는 툴은 Windows 위에서 돌 수도 있음. 헬퍼 빌드가 이 패키지
// 빌드에 들어갈 수는 없으니, 빌드 스크립트가 먼저 컴파일해서 이 자리에
// 떨어뜨리고, 이 패키지는 그게 진짜 ELF 인지만 확인.
package initbin

import (
	_ "embed"
	"errors"
	"fmt"
)

//go:embed vibeldr-init.bin
var binary []byte

// elfMagic is the first four bytes of any ELF executable.
// elfMagic - 모든 ELF 실행 파일의 첫 네 바이트.
var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// ErrNotBuilt reports that the placeholder is still in place.
// ErrNotBuilt - 자리표시자가 아직 그대로라는 뜻.
var ErrNotBuilt = errors.New("the ramdisk helper has not been compiled into this build")

// Binary returns the helper.
// Binary - 헬퍼 바이너리를 돌려준다.
func Binary() ([]byte, error) {
	if len(binary) < len(elfMagic) || string(binary[:4]) != string(elfMagic) {
		return nil, fmt.Errorf("%w: run `vibeldr bootstrap` from the source tree, or build it with GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o internal/initbin/vibeldr-init.bin ./cmd/vibeldr-init", ErrNotBuilt)
	}
	return binary, nil
}
