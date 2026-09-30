//go:build linux

package main

// kmsg_linux.go copies the loader's own log lines into the kernel log.
//
// The loader's lines go to standard output, and on a machine with no serial
// port that reaches nothing. The kernel log is kept whatever the console, and
// it can be read afterwards from dmesg, over netconsole (netconsole.go), or out
// of the guest's memory when a boot under a hypervisor stops. Written there, the
// loader's lines sit in order between the kernel's own, so where a stuck boot
// stopped shows up as the last line.
//
// Every stage of the boot is a separate run of this program, and by -pivot
// /dev is gone, so the kmsg device is opened through a private node when
// /dev/kmsg is not there.
//
// kmsg_linux.go - 로더 자신의 로그 줄을 커널 로그에 복사한다.
//
// 로더의 줄은 표준 출력으로 가는데, 시리얼 포트가 없는 기계에서는 그게 아무
// 데도 닿지 않는다. 커널 로그는 콘솔과 상관없이 남고, 나중에 dmesg 로도,
// netconsole 로도 (netconsole.go), 하이퍼바이저 위에서 멈춘 부팅이라면 게스트
// 메모리에서도 읽을 수 있다. 거기에 쓰면 로더의 줄이 커널 자신의 줄 사이에
// 순서대로 놓여서, 멈춘 부팅이 어디서 멈췄는지가 마지막 줄로 드러난다.
//
// 부팅의 각 단계는 이 프로그램의 별개 실행이고 -pivot 때는 /dev 가 없으므로,
// /dev/kmsg 가 없으면 전용 노드를 거쳐 kmsg 장치를 연다.

import (
	"os"
	"sync"
	"syscall"
)

// privateKmsg is the node made when /dev/kmsg is gone. It is removed as soon
// as it is open.
//
// privateKmsg - /dev/kmsg 가 없을 때 만드는 노드. 열자마자 지운다.
const privateKmsg = "/.vibeldr-kmsg"

var (
	kmsgMu    sync.Mutex
	kmsgTried bool
	kmsgFile  *os.File
)

// mirrorLine writes one line to the kernel log.
// mirrorLine - 한 줄을 커널 로그에 쓴다.
func mirrorLine(s string) {
	kmsgMu.Lock()
	if !kmsgTried {
		kmsgTried = true
		openKmsg()
	}
	f := kmsgFile
	kmsgMu.Unlock()
	if f != nil {
		// <6> is KERN_INFO.
		// <6> 은 KERN_INFO.
		_, _ = f.WriteString("<6>" + s + "\n")
	}
}

func openKmsg() {
	// Without this the kernel drops lines from /dev/kmsg past a small burst,
	// and the lines lost would be the ones around a failure.
	//
	// 이게 없으면 커널이 /dev/kmsg 의 줄을 짧은 폭주 뒤부터 버리는데, 잃는 줄이
	// 하필 실패 언저리의 것이다.
	_ = os.WriteFile("/proc/sys/kernel/printk_devkmsg", []byte("on\n"), 0o644)
	if f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0); err == nil {
		kmsgFile = f
		return
	}
	_ = os.Remove(privateKmsg)
	// char 1:11 is the kernel's kmsg device.
	// char 1:11 은 커널의 kmsg 장치다.
	if err := syscall.Mknod(privateKmsg, syscall.S_IFCHR|0o600, int(mkdev(1, 11))); err != nil {
		return
	}
	kmsgFile, _ = os.OpenFile(privateKmsg, os.O_WRONLY, 0)
	_ = os.Remove(privateKmsg)
}
