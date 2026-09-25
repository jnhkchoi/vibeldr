//go:build linux

// loaderbus_linux.go works out which bus the loader disk is attached to.
//
// Why it is needed:
//
//	Given `synoboot_satadom=`, a 4.4-line kernel (DS918+, DS3622xs+) believes the
//	loader is on a SATA DOM and maps the synoboot partitions on that assumption.
//	If the loader is actually on another bus, virtio-scsi say, the mapping never
//	happens and /dev/synoboot1..4 end up as shells with no real partition behind
//	them. Installing DSM in that state fails outright at the last step, unable to
//	mount the boot partition:
//
//	    updater: failed to mount boot device /dev/synoboot2 /tmp/bootmnt (errno:6)
//	    updater: Failed to mount boot partition
//	    Failed to accomplish the update! (errno = 21)
//
//	The web assistant shows that as "the file appears to be corrupted", which
//	points at something else entirely. So the bus is checked rather than guessed.
//
// loaderbus_linux.go - 로더 디스크가 어떤 버스에 붙어 있는지 알아낸다.
//
// 왜 필요한가:
//
//	커널 4.4 계열(DS918+, DS3622xs+)에 `synoboot_satadom=` 을 주면 DSM 은 로더가
//	SATA DOM 에 있다고 믿고 그 전제로 synoboot 파티션을 매핑한다. 로더가 실제로는
//	virtio-scsi 같은 다른 버스에 있으면 매핑이 이루어지지 않아 /dev/synoboot1..4 가
//	실제 파티션 없는 껍데기가 된다. 그 상태로 DSM 을 설치하면 마지막에 부트
//	파티션을 마운트하지 못해 설치가 통째로 실패한다. 오류 세 줄은 위 영문과 같다.
//
//	웹 어시스턴트는 이걸 "파일이 손상된 것 같습니다" 로 표시해서 원인이 전혀 달라
//	보인다. 그래서 버스를 넘겨짚지 말고 실제로 확인한다.
package main

import (
	"os"
	"path/filepath"
	"strings"
)

// loaderBus decides the bus from the device path /sys/block/<name> points at.
//
// What comes back is the name cmdline.Options.LoaderBus uses: "sata", "usb",
// "nvme", "virtio" or "mmc". An undecidable case comes back empty, and the
// caller then does not claim a SATA DOM.
//
// loaderBus - /sys/block/<이름> 이 가리키는 장치 경로로 버스를 판별한다.
//
// 돌려주는 값은 cmdline.Options.LoaderBus 가 쓰는 이름: "sata", "usb", "nvme",
// "virtio", "mmc". 판별할 수 없으면 빈 문자열이고, 그때 호출자는 SATA DOM 이라고
// 주장하지 않는다.
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
