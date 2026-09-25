package ramdisk

// console.go keeps the ramdisk's boot script from depending on a console, on
// the kernels where a console is not guaranteed.
//
// Synology's busybox starts the ramdisk's boot script with a line built into
// its init:
//
//	/bin/ash /linuxrc.syno > /dev/console 2>&1
//
// On a 5.x DSM kernel the only console is a serial port the 8250 driver
// actually detected - Synology took out the VT console and the unconditional
// 8250 one. With no UART (a VM with no COM port, a mini PC with it disabled in
// the BIOS) /dev/console cannot be opened, the shell refuses to run a command
// whose redirection fails, and linuxrc.syno never starts. init goes straight
// to its sysinit entry, /etc/rc, which is the installer: the machine comes up
// in junior mode, before the loader's own ramdisk hook ever runs, so none of
// its drivers load and the network never comes up. It looks like a hang.
//
// The fix points that redirection at a file instead, the same length so the
// binary is otherwise untouched. The script's output is lost to the console
// either way when there is no console; where there is one, the loader's own
// lines still reach it through the kernel log. It is made on 5.x kernels only:
// a 4.4 kernel registers its 8250 console unconditionally, so /dev/console
// always opens there.
//
// console.go - 콘솔이 보장되지 않는 커널에서, 램디스크의 부팅 스크립트가
// 콘솔에 의존하지 않게 한다.
//
// 시놀로지 busybox 는 init 에 박힌 한 줄로 램디스크 부팅 스크립트를 시작한다
// (위 영문 주석의 명령). 5.x DSM 커널의 콘솔은 8250 드라이버가 실제로 검출한
// 시리얼 포트 하나뿐이다 - 시놀로지가 VT 콘솔과 조건 없는 8250 콘솔을 뺐다.
// UART 가 없으면 (COM 포트 없는 VM, BIOS 에서 끈 미니 PC) /dev/console 을 열 수
// 없고, 셸은 리디렉션이 실패한 명령을 실행하지 않으므로 linuxrc.syno 가 아예
// 시작하지 않는다. init 은 곧장 sysinit 항목인 /etc/rc 로 가는데 그게
// 설치기다. 기계가 junior 모드로 뜨고, 그건 로더 자신의 램디스크 훅이 돌기도
// 전이라 드라이버가 하나도 안 올라가고 네트워크도 안 뜬다. 멈춘 것처럼 보인다.
//
// 수정은 그 리디렉션을 파일로 돌린다. 길이가 같아서 바이너리의 나머지는
// 그대로다. 콘솔이 없으면 스크립트 출력은 어차피 콘솔로 못 가고, 콘솔이 있는
// 곳에서는 로더 자신의 줄이 커널 로그를 거쳐 여전히 닿는다. 이 수정은 5.x
// 커널에서만 한다. 4.4 커널은 8250 콘솔을 조건 없이 등록해서 거기서는
// /dev/console 이 항상 열린다.

import (
	"bytes"
	"strconv"
)

const busyboxName = "usr/bin/busybox"

var (
	linuxrcToConsole = []byte("/bin/ash /linuxrc.syno > /dev/console 2>&1")
	linuxrcToFile    = []byte("/bin/ash /linuxrc.syno > /var/log/lrc 2>&1")
)

// LinuxrcLog is where the boot script's output goes after the change.
// LinuxrcLog - 수정 뒤 부팅 스크립트 출력이 가는 곳.
const LinuxrcLog = "/var/log/lrc"

// freeLinuxrcFromConsole makes the change on a 5.x ramdisk and reports whether
// it was made. A 4.4 ramdisk, or a busybox without that line, is left alone.
//
// freeLinuxrcFromConsole - 5.x 램디스크에 수정을 하고 했는지를 돌려준다. 4.4
// 램디스크나 그 줄이 없는 busybox 는 건드리지 않는다.
func (a *Archive) freeLinuxrcFromConsole() bool {
	if a.kernelMajor() < 5 {
		return false
	}
	e, ok := a.Get(busyboxName)
	if !ok || !bytes.Contains(e.Data, linuxrcToConsole) {
		return false
	}
	e.Data = bytes.ReplaceAll(e.Data, linuxrcToConsole, linuxrcToFile)
	return true
}

// kernelMajor reads the kernel's major version off the first module in the
// ramdisk, from its vermagic. It is 0 when there is no module to read.
//
// kernelMajor - 램디스크의 첫 모듈 vermagic 에서 커널 주 버전을 읽는다. 읽을
// 모듈이 없으면 0.
func (a *Archive) kernelMajor() int {
	key := []byte("vermagic=")
	for _, e := range a.Entries {
		if !e.IsRegular() || !bytes.HasSuffix([]byte(e.Name), []byte(".ko")) {
			continue
		}
		i := bytes.Index(e.Data, key)
		if i < 0 {
			continue
		}
		v := e.Data[i+len(key):]
		j := bytes.IndexByte(v, '.')
		if j <= 0 {
			continue
		}
		if n, err := strconv.Atoi(string(v[:j])); err == nil {
			return n
		}
	}
	return 0
}
