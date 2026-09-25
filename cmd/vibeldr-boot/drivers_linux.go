//go:build linux

package main

import (
	"strings"
	"time"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
)

// bootstrapModules are the modules loaded by name first, because device
// matching never reaches them.
//
// There are three kinds.
//
// (1) Upper-layer drivers. sd_mod is the common layer that turns a block disk
// into /dev/sd* whether it came over SATA, SCSI, USB or virtio, and it has no
// PCI device of its own to match a modalias against. It attaches by itself when
// a controller below it (ahci, virtio_scsi, usb_storage and so on) enumerates
// its targets, so loading it up front means neither the kind of controller nor
// the timing of that enumeration matters. Without it the block device never
// appears, however well the disk controller is working.
//
// (2) Filesystems. vfat and nls do not hang off a device at all, so there is
// nothing to match in the first place. Without them the kernel answers a mount
// with EINVAL, which reads like a bad argument but means "never heard of that
// filesystem".
//
// (3) Input and display. The installer screen is drawn on the framebuffer and
// driven with a mouse, so both are needed. evdev matters most: the keyboard and
// mouse drivers can come up perfectly and, without evdev, the
// /dev/input/event* nodes are never created and there is no way to read the
// input at all. The display drivers are usually built into the kernel and need
// not be listed, but the names are here for builds that have them as modules.
// A name that is not in the pack is skipped silently, so listing them all costs
// nothing.
//
// bootstrapModules - 장치 매칭으로는 잡히지 않아 이름으로 먼저 올리는 모듈들.
// 세 부류이고 각각의 이유는 위 영문과 같다.
var bootstrapModules = []string{
	"sd_mod", "sr_mod",
	"usb_common", "usbcore", "xhci_hcd", "xhci_pci", "ehci_hcd", "ehci_pci",
	"uhci_hcd", "usb_storage", "uas",
	"fat", "vfat", "nls_cp437", "nls_iso8859_1", "nls_utf8",
	// Input: evdev makes the event nodes, then the PS/2 and USB paths below it.
	// 입력: evdev 가 event 노드를 만들고, 그 아래로 PS/2 와 USB 경로.
	"evdev", "serio", "i8042", "atkbd", "psmouse",
	"hid", "hid_generic", "usbhid",
	// Display: from the ones that keep the mode the firmware set, to the
	// per-device drivers.
	//
	// 화면: 펌웨어가 세운 모드를 그대로 쓰는 것부터, 장치별 드라이버까지.
	"simplefb", "simpledrm", "efifb", "vesafb",
	"bochs", "bochs_drm", "virtio_gpu", "qxl", "vmwgfx",
}

// loadDrivers turns on whatever is actually attached to this machine.
//
// It is the same matching the loader does inside the DSM ramdisk, and it
// matters more here: with no network card there is no way to download the .pat,
// and this environment has no udev to notice the card.
//
// Why it takes several passes: loading one driver makes new devices appear
// behind it. A virtio disk or network card is two steps back - virtio_pci
// claims the PCI device and creates the virtio bus, and only then does a device
// matching virtio_scsi's or virtio_net's alias show up in sysfs. One pass
// catches only the first step. The same goes for every bridge, mux and
// controller that hangs devices below it.
//
// loadDrivers - 이 머신에 실제로 붙어있는 걸 켠다.
//
// DSM 램디스크 안에서 로더가 하는 매칭과 같은 방식이다. 여기서는 더 중요하다.
// 네트워크 카드가 없으면 .pat 을 다운로드할 방법이 없고, 이 환경엔 카드를
// 알아채줄 udev 도 없다.
//
// 여러 패스를 도는 이유: 드라이버 하나를 로드하면 그 뒤로 새 장치가 나타난다.
// virtio 디스크·네트워크는 두 계단 뒤에 있다 - virtio_pci 가 PCI 장치를 잡아
// virtio 버스를 만들고, 그제서야 virtio_scsi / virtio_net 의 alias 와 매칭될
// 장치가 sysfs 에 나타난다. 한 패스로는 첫 계단만 잡는다. 밑에 장치를 매다는
// 브리지·먹스·컨트롤러도 전부 마찬가지다.
func loadDrivers() (int, error) {
	index, err := kmod.Scan(kmod.SearchDirs...)
	if err != nil {
		return 0, err
	}

	// An empty pass does not stop the loop, because device registration is
	// asynchronous: in the pass right after virtio_scsi loads there is no
	// SCSI target in sysfs yet and nothing to match, and only once the target
	// appears a moment later does sd_mod match. Stopping on an empty pass
	// means never finding the disk.
	//
	// 빈 패스가 나와도 멈추지 않는다. 장치 등록이 비동기라서 그렇다:
	// virtio_scsi 를 로드한 직후의 패스에서는 SCSI 타겟이 아직 sysfs 에
	// 없어 매칭될 게 없지만, 조금 뒤 타겟이 나타나면 그제서야 sd_mod 가
	// 매칭된다. 빈 패스에서 끊으면 디스크를 영영 못 잡는다.
	const passes = 4
	var ok []string
	for _, m := range index.ByName(bootstrapModules...) {
		if err := kmod.Load(m); err == nil {
			ok = append(ok, m.Name)
		}
	}
	for pass := 1; pass <= passes; pass++ {
		aliases, err := kmod.DeviceAliases(hwscan.DefaultSysfs)
		if err != nil {
			return len(ok), err
		}
		for _, m := range index.Resolve(aliases, kmod.Loaded("/proc/modules")) {
			if err := kmod.Load(m); err == nil {
				ok = append(ok, m.Name)
			}
		}
		if pass < passes {
			// Room for the kernel to register what the drivers just loaded
			// have found.
			//
			// 방금 로드한 드라이버가 찾아낸 것을 커널이 등록할 여유.
			time.Sleep(time.Second)
		}
	}

	if len(ok) > 0 {
		say("drivers: %s", strings.Join(ok, " "))
	}
	return len(ok), nil
}
