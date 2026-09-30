//go:build linux

// loaderbus_linux.go works out which bus the loader disk is attached to, so
// the wizard can warn before the install when it is not USB.
//
// DSM makes /dev/synoboot* only for a boot disk it recognises, and a loader on
// another bus - virtio-scsi, NVMe, SATA - is not one of them. The install then
// runs to its very end and fails there, unable to mount the boot partition:
//
//	updater: failed to mount boot device /dev/synoboot2 /tmp/bootmnt (errno:6)
//	updater: Failed to mount boot partition
//	Failed to accomplish the update! (errno = 21)
//
// The web assistant shows that as "the file appears to be corrupted", which
// points at something else entirely.
//
// loaderbus_linux.go - 로더 디스크가 어떤 버스에 붙어 있는지 알아내, USB 가
// 아니면 마법사가 설치 전에 경고할 수 있게 한다.
//
// DSM 은 자기가 알아보는 부트 디스크에만 /dev/synoboot* 를 만들고, 다른 버스
// (virtio-scsi, NVMe, SATA) 의 로더는 거기 들지 않는다. 그러면 설치가 끝까지
// 돈 뒤 마지막에 부트 파티션을 마운트하지 못해 실패한다. 오류 세 줄은 위 영문과
// 같다. 웹 어시스턴트는 이걸 "파일이 손상된 것 같습니다" 로 표시해서 원인이
// 전혀 달라 보인다.
package main

import (
	"os"
	"path/filepath"
	"strings"
)

// loaderBus decides the bus from the device path /sys/block/<name> points at.
//
// It returns "sata", "usb", "nvme", "virtio" or "mmc", or "" when the path
// does not tell.
//
// loaderBus - /sys/block/<이름> 이 가리키는 장치 경로로 버스를 판별한다.
//
// "sata", "usb", "nvme", "virtio", "mmc" 중 하나를, 경로로 알 수 없으면 "" 를
// 돌려준다.
func loaderBus(kernelName string) string {
	if kernelName == "" {
		return ""
	}
	target, err := os.Readlink(filepath.Join("/sys/block", kernelName))
	if err != nil {
		return ""
	}
	p := filepath.ToSlash(target)
	switch {
	// USB comes first: a USB-SATA bridge shows both ata and usb in the path,
	// and the USB reading is the one DSM should get.
	//
	// USB 가 가장 먼저: USB-SATA 브리지는 경로에 ata 와 usb 가 함께 나오는데,
	// DSM 에게는 USB 로 보이는 쪽이 맞다.
	case strings.Contains(p, "/usb"):
		return "usb"
	case strings.Contains(p, "/nvme"):
		return "nvme"
	case strings.Contains(p, "/mmc"):
		return "mmc"
	// virtio is checked before ata. A virtio-scsi device path also carries
	// host/target, but never ata, so keeping the order is enough.
	//
	// virtio 를 ata 보다 먼저 본다. virtio-scsi 장치 경로에도 host/target 이
	// 나오지만 ata 는 안 나오므로 순서만 지키면 충분하다.
	case strings.Contains(p, "/virtio"):
		return "virtio"
	case strings.Contains(p, "/ata"):
		return "sata"
	}
	return ""
}
