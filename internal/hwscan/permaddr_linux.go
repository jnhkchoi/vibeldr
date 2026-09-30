//go:build linux

package hwscan

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// permAddr reads an interface's permanent (factory) MAC address with the
// ETHTOOL_GPERMADDR ioctl. The kernel keeps it apart from the current
// address, so it stays the same after the loader has written another MAC onto
// the card, and it moves with the card to another slot or port. It returns ""
// when the driver does not report one or reports all zeroes.
//
// permAddr - 인터페이스의 영구(공장) MAC 주소를 ETHTOOL_GPERMADDR ioctl 로
// 읽는다. 커널은 이것을 지금 주소와 따로 들고 있어서, 로더가 카드에 다른 MAC 을
// 써도 그대로이고, 카드를 다른 슬롯이나 포트로 옮겨도 따라간다. 드라이버가 알려
// 주지 않거나 전부 0 이면 "" 를 돌려준다.
func permAddr(iface string) string {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return ""
	}
	defer syscall.Close(fd)

	// struct ethtool_perm_addr { u32 cmd; u32 size; u8 data[]; }
	const ethtoolGPermAddr, siocEthtool, maxAddr = 0x20, 0x8946, 32
	// On the heap, which does not move, and kept alive past the call.
	// 옮겨지지 않는 힙에 두고, 호출이 끝날 때까지 살려 둔다.
	buf := make([]byte, 8+maxAddr)
	binary.LittleEndian.PutUint32(buf[0:4], ethtoolGPermAddr)
	binary.LittleEndian.PutUint32(buf[4:8], maxAddr)
	var req struct {
		name [16]byte
		data uintptr
		_    [16]byte
	}
	copy(req.name[:], iface)
	req.data = uintptr(unsafe.Pointer(&buf[0]))
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocEthtool, uintptr(unsafe.Pointer(&req)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		return ""
	}
	n := int(binary.LittleEndian.Uint32(buf[4:8]))
	if n != 6 {
		return ""
	}
	mac := buf[8:14]
	zero := true
	for _, b := range mac {
		if b != 0 {
			zero = false
		}
	}
	if zero {
		return ""
	}
	return fmt.Sprintf("%02x%02x%02x%02x%02x%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}
