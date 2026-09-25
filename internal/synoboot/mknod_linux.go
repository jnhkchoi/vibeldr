//go:build linux

package synoboot

import (
	"os"
	"syscall"
)

// mknodBlock creates a block device node, replacing any node already there.
// mknodBlock - 블록 장치 노드를 만든다. 이미 있으면 교체한다.
func mknodBlock(path string, major, minor uint32) error {
	// A node left over from an earlier boot would point at whatever disk held
	// that minor number then, so it is removed rather than reused.
	//
	// 이전 부팅에서 남은 노드는 그때 그 minor 번호를 가졌던 디스크를
	// 가리키므로, 재사용하지 않고 지운 뒤 다시 만든다.
	_ = os.Remove(path)
	return syscall.Mknod(path, syscall.S_IFBLK|0o660, int(makedev(major, minor)))
}
