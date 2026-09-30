//go:build linux

package synoboot

import (
	"os"
	"syscall"
	"unsafe"
)

// blkROSet is BLKROSET from linux/fs.h, _IO(0x12, 93).
// blkROSet - linux/fs.h 의 BLKROSET, _IO(0x12, 93).
const blkROSet = 0x125d

// clearReadOnly clears the kernel's read-only mark on the disk at node. The
// kernel keeps its own mark apart from the device's switch, and some card
// readers leave it set; with the switch off, clearing it makes the disk
// writable again.
//
// clearReadOnly - node 디스크에 커널이 붙인 읽기 전용 표시를 지운다. 커널은 장치
// 스위치와 따로 자기 표시를 두고, 일부 카드 리더는 그것을 켠 채로 둔다. 스위치가
// 꺼져 있으면 표시를 지우는 것만으로 다시 쓸 수 있게 된다.
func clearReadOnly(node string) error {
	f, err := os.Open(node)
	if err != nil {
		return err
	}
	defer f.Close()
	var off int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkROSet, uintptr(unsafe.Pointer(&off))); e != 0 {
		return e
	}
	return nil
}
