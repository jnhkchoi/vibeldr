//go:build linux

package main

// stdio_linux.go makes sure this program starts with file descriptors 0, 1 and
// 2 open, whatever the kernel handed it.
//
// The kernel gives /init its standard descriptors by opening /dev/console. When
// that fails it prints "Warning: unable to open an initial console" and starts
// /init anyway, with 0, 1 and 2 closed. On a Synology kernel that is not a rare
// corner: CONFIG_VGA_CONSOLE and CONFIG_FRAMEBUFFER_CONSOLE are off, so tty0 is
// the dummy console and cannot back /dev/console, which leaves the serial port
// as the only real one. Take the serial port away - a virtual machine with no
// COM port, a mini PC whose chipset exposes none - and there is no console at
// all:
//
//	Console: colour dummy device 80x25
//	Warning: unable to open an initial console.
//
// Writing to a closed descriptor only fails, which would be harmless. The
// danger is the next open: the kernel hands out the lowest free descriptor, so
// the first file this program opens becomes descriptor 1 and every later line
// of output is written into it. The first thing opened here is a block device -
// the loader's own disk, while looking for its label - so the log would be
// written onto the disk itself.
//
// Three descriptors on /dev/null cost nothing and remove the whole class of
// failure. The real record of the boot is the copy written to the loader's disk
// by flushLog, which does not depend on any of this.
//
// stdio_linux.go - 커널이 뭘 넘겨줬든 이 프로그램이 파일 서술자 0, 1, 2 를
// 갖고 시작하도록 보장한다.
//
// 커널은 /dev/console 을 열어 /init 에게 표준 서술자를 준다. 그게 실패하면
// "Warning: unable to open an initial console" 을 찍고 0, 1, 2 가 닫힌 채로
// /init 을 그냥 시작한다. 시놀로지 커널에서는 이게 드문 구석이 아니다.
// CONFIG_VGA_CONSOLE 과 CONFIG_FRAMEBUFFER_CONSOLE 이 꺼져 있어 tty0 은 더미
// 콘솔이라 /dev/console 을 받쳐주지 못하고, 결국 시리얼 포트만이 진짜 콘솔이다.
// 그 시리얼을 떼면 - COM 포트 없는 가상머신, 칩셋이 노출하지 않는 미니 PC -
// 콘솔이 하나도 없는 상태가 된다:
//
//	Console: colour dummy device 80x25
//	Warning: unable to open an initial console.
//
// 닫힌 서술자에 쓰는 것 자체는 그냥 실패할 뿐이라 해롭지 않다. 위험한 건 그
// 다음의 open 이다. 커널은 비어 있는 가장 낮은 번호를 내주므로, 이 프로그램이
// 처음 여는 파일이 1 번이 되고 그 뒤의 모든 출력이 그리로 들어간다. 여기서
// 처음 여는 것은 블록 장치다. 로더가 자기 디스크의 라벨을 찾느라 여는 것이라,
// 로그가 디스크 위에 쓰이게 된다.
//
// /dev/null 세 개는 아무 비용도 아니면서 이 부류의 실패를 통째로 없앤다.
// 부팅의 진짜 기록은 flushLog 가 로더 디스크에 남기는 사본이고, 그건 이것과
// 무관하게 동작한다.

import (
	"os"
	"syscall"
)

// ensureStdio fills in whichever of 0, 1 and 2 the kernel left closed.
//
// A descriptor is judged closed by asking the kernel about it rather than by
// writing to it, because a write to a console that exists but cannot drain
// would not come back.
//
// ensureStdio - 커널이 닫아 둔 채로 넘긴 0, 1, 2 를 채운다.
//
// 닫혔는지는 써 보지 않고 커널에 물어서 판단한다. 존재하지만 빠져나가지
// 못하는 콘솔에 써 보면 그 write 가 돌아오지 않기 때문이다.
func ensureStdio() {
	var missing []int
	for fd := 0; fd <= 2; fd++ {
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0); errno != 0 {
			missing = append(missing, fd)
		}
	}
	if len(missing) == 0 {
		return
	}
	nul, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	for _, fd := range missing {
		_ = syscall.Dup2(int(nul.Fd()), fd)
	}
	// The handle itself is only needed as a source to copy from. Closing it
	// when it landed on 0, 1 or 2 would undo the work.
	//
	// 핸들 자체는 복제할 원본으로만 필요하다. 그게 0, 1, 2 중 하나에 놓였다면
	// 닫는 순간 방금 한 일이 무효가 된다.
	if nul.Fd() > 2 {
		_ = nul.Close()
	}
}
