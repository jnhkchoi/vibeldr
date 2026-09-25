//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
	"vibeldr/internal/ramdisk"
)

// Drivers the ramdisk does not have, kept on a partition with no filesystem.
//
// Synology's ramdisk carries 47 modules: the ones the appliance this model was
// built as actually has. A machine with a Realtek card, or an Aquantia card, or
// an LSI controller finds nothing that matches and comes up with no network and
// sometimes no disks - and there is no way to install DSM from a machine that
// cannot reach the internet. So the loader carries several hundred more.
//
// They are not kept on a FAT partition alongside the kernel, tidy as that
// would be, because Synology's kernel refuses every vfat mount during the
// ramdisk stage: EINVAL, no message in the log, while the same image mounts
// perfectly on an ordinary Linux machine.
//
// Rather than argue with that, the loader stops needing a filesystem. The pack
// is one cpio archive written straight onto a partition of its own, and this
// reads it off the block device and unpacks it in memory. Only the modules that
// match something get loaded; the rest are read and dropped. The archive format
// is the one the kernel uses for ramdisks, which the loader already reads and
// writes, and the modules are loaded with init_module - the call that takes an
// image rather than a file, which is exactly what there is here.
//
// The order matters. The pack is on the loader's own disk, and on a USB loader
// that disk does not exist until usb-storage is loaded, which happens in the
// first pass from the ramdisk's own modules. So: ramdisk drivers, then the disk
// they make visible, then everything else.
//
// 램디스크에 없는 드라이버를, 파일시스템 없는 파티션에 담아 둔다.
//
// 시놀로지 램디스크에는 모듈이 47 개 있다. 이 모델의 원래 기기가 실제로 가진
// 것들이다. Realtek 카드, Aquantia 카드, LSI 컨트롤러가 달린 머신은 맞는
// 것을 찾지 못해 네트워크 없이, 때로는 디스크도 없이 올라온다. 인터넷에 닿지
// 못하는 머신에서는 DSM 을 설치할 방법이 없다. 그래서 로더가 수백 개를 더
// 싣고 다닌다.
//
// 커널 옆 FAT 파티션에 두면 깔끔하겠지만 그렇게 하지 않는다. 시놀로지 커널은
// 램디스크 단계에서 모든 vfat 마운트를 거부한다: EINVAL, 로그에 메시지 없음.
// 같은 이미지가 보통 리눅스 머신에서는 멀쩡히 마운트된다.
//
// 그와 싸우는 대신 로더는 파일시스템이 필요 없게 한다. 팩은 cpio 아카이브
// 하나를 전용 파티션에 그대로 쓴 것이고, 여기서 블록 장치로 읽어 메모리에서
// 푼다. 무언가와 매칭되는 모듈만 로드하고 나머지는 읽고 버린다. 아카이브
// 형식은 커널이 램디스크에 쓰는 것이라 로더가 이미 읽고 쓸 줄 안다. 모듈은
// init_module 로 로드한다 - 파일이 아니라 이미지를 받는 호출이라 여기 있는
// 것과 딱 맞는다.
//
// 순서가 중요하다. 팩은 로더 자신의 디스크에 있고, USB 로더에서 그 디스크는
// usb-storage 가 로드되기 전에는 존재하지 않는다. usb-storage 는 램디스크 자체
// 모듈로 도는 첫 패스에서 올라온다. 그래서 램디스크 드라이버, 그것이 보이게
// 한 디스크, 나머지 전부 순이다.

const (
	// packDevice is the node made for the archive partition.
	// packDevice - 아카이브 파티션용으로 만드는 노드.
	packDevice = hwscan.DefaultDev + "/vibeldr-pack"
	// loaderPayload is the loader's third partition. The archive is on the
	// fourth, which is the next minor number along - MBR partitions of one
	// disk are numbered in order.
	//
	// loaderPayload - 로더의 세 번째 파티션. 아카이브는 네 번째에 있고, 그
	// 다음 minor 번호다 - 한 디스크의 MBR 파티션은 순서대로 번호가 붙는다.
	loaderPayload = hwscan.DefaultDev + "/synoboot3"
	// packMaxBytes guards against reading a whole disk into memory if the
	// partition turns out not to be what is expected.
	//
	// packMaxBytes - 파티션이 예상과 다를 때 디스크 전체를 메모리로 읽는 것을
	// 막는다.
	packMaxBytes = 512 << 20
)

// readPackUpToTrailer reads only as much of the pack partition as the archive
// actually wrote.
//
// Reading the whole partition would be wrong. The partition can be far larger
// than the pack, with nothing but zeros after it, and memory is not plentiful
// during the ramdisk stage. A cpio's last entry is named "TRAILER!!!", so
// seeing that ends the read.
//
// readPackUpToTrailer - 팩 파티션에서 아카이브가 실제로 쓴 만큼만 읽는다.
//
// 파티션 전체를 읽으면 안 된다. 파티션은 팩보다 훨씬 클 수 있고(뒤쪽은 전부
// 0), 램디스크 단계는 메모리가 넉넉하지 않다. cpio 는 마지막 항목 이름이
// "TRAILER!!!" 이므로 그것이 보이면 거기서 멈춘다.
func readPackUpToTrailer(dev string) ([]byte, error) {
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	const chunk = 8 << 20
	var buf []byte
	for {
		b := make([]byte, chunk)
		n, err := io.ReadFull(f, b)
		if n > 0 {
			buf = append(buf, b[:n]...)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Once the trailer turns up, keep to the end of that record and stop.
		// 트레일러가 나오면 그 레코드 끝까지만 남기고 중단한다.
		if i := bytes.Index(buf, []byte("TRAILER!!!")); i >= 0 {
			end := i + len("TRAILER!!!")
			if end+4 <= len(buf) {
				end += 4 // room for the padding after the name / 이름 뒤 패딩 여유
			}
			return buf[:end], nil
		}
		// No trailer this far means the partition is not a pack.
		// 여기까지 트레일러가 없으면 팩이 아니다.
		if len(buf) > packMaxBytes {
			return nil, fmt.Errorf("no cpio trailer in the first %d MiB", packMaxBytes>>20)
		}
	}
	if i := bytes.Index(buf, []byte("TRAILER!!!")); i >= 0 {
		return buf[:i+len("TRAILER!!!")], nil
	}
	return buf, nil
}

// loadExtraDrivers reads the driver pack and loads what this machine needs.
// loadExtraDrivers - 드라이버 팩을 읽어 이 머신에 필요한 걸 로드한다.
func loadExtraDrivers() {
	dev, err := packPartition()
	if err != nil {
		// An image built without a pack is perfectly normal, but if that
		// cannot be told apart from failing to read a pack that should be
		// there, the cause is untraceable. Leave the reason.
		//
		// 팩 없이 빌드한 이미지는 정상이지만, 있어야 할 팩을 못 읽는 것과
		// 구분이 안 되면 원인을 찾을 수 없다. 이유를 남긴다.
		logf("extra drivers: cannot read partition 4: %v", err)
		return
	}
	defer os.Remove(dev)

	archive, err := readPackUpToTrailer(dev)
	if err != nil {
		logf("extra drivers: %s: %v", dev, err)
		return
	}
	a, err := ramdisk.ReadCPIO(archive)
	if err != nil {
		logf("extra drivers: %s holds no archive: %v", dev, err)
		return
	}

	// The blacklist the command line already advertises applies to the pack
	// too. modprobe.blacklist is only read by userspace modprobe, and this
	// code calls init_module directly, so nothing would honour it here - a
	// module the kernel reports as blocked would still be loaded.
	//
	// 커맨드라인이 이미 광고하고 있는 차단 목록을 팩에도 적용한다.
	// modprobe.blacklist 는 userspace modprobe 만 보는 값이고 여기는
	// init_module 을 직접 부르므로, 이 줄이 없으면 커널이 "차단했다" 고
	// 적은 모듈이 그대로 로드된다.
	blocked := blacklistedModules()

	// The firmware in the pack is not all unpacked here. The DSM ramdisk root
	// is RAM, so unpacking about 100MB whole - as epyc7002's would - costs that
	// much on a machine with little of it. Only what the modules actually loaded
	// ask for is taken.
	//
	// 팩 안의 펌웨어는 여기서 다 풀지 않는다. DSM 램디스크 루트는 RAM 이라,
	// epyc7002 팩처럼 100MB 가량을 통째로 풀면 저사양 기계에서 그만큼을 잃는다.
	// 실제로 로드하는 모듈이 요구하는 것만 골라 쓴다.
	firmware := map[string][]byte{}
	// The modules in the pack that are Synology's own, listed by the pack build
	// (vibeldr-modules tools/merge_originals.py). When one of them and one of
	// ours claim the same device, only Synology's is loaded for it.
	//
	// 팩 안에서 시놀 자신의 모듈 목록 (vibeldr-modules 의 merge_originals.py 가 적는다).
	// 그것과 우리 모듈이 같은 장치를 잡으면 그 장치에는 시놀 것만 올린다.
	originals := map[string]bool{}
	for _, e := range a.Entries {
		if e.IsRegular() && strings.HasPrefix(e.Name, firmwarePrefix) {
			firmware[strings.TrimPrefix(e.Name, firmwarePrefix)] = e.Data
		}
		if e.IsRegular() && path.Base(e.Name) == originalsList {
			for _, n := range strings.Fields(string(e.Data)) {
				originals[strings.ReplaceAll(n, "-", "_")] = true
			}
		}
	}

	index := kmod.Index{}
	images := map[string][]byte{}
	for _, e := range a.Entries {
		if !e.IsRegular() || !strings.HasSuffix(e.Name, ".ko") {
			continue
		}
		// A pack can be flat (`r8125.ko`) or carry the kernel tree's structure
		// (`kernel/drivers/net/.../r8125.ko`). A module's name comes from its file
		// name alone, so the path is stripped before it is passed on. Left attached,
		// the name becomes "kernel/drivers/acpi/button" and the load fails.
		//
		// 팩은 평면(`r8125.ko`) 일 수도, 커널 트리 구조를 그대로 담은
		// (`kernel/drivers/net/.../r8125.ko`) 것일 수도 있다. 모듈 이름은
		// 파일 이름만으로 정해지므로 경로를 떼고 넘긴다. 경로가 붙은 채로
		// 넘기면 이름이 "kernel/drivers/acpi/button" 이 되어 로드에 실패한다.
		m, err := kmod.ReadBytes(path.Base(e.Name), e.Data)
		if err != nil {
			continue
		}
		if _, dup := index[m.Name]; dup {
			continue
		}
		if blocked[m.Name] {
			continue
		}
		index[m.Name] = m
		images[m.Name] = e.Data
	}
	if len(index) == 0 {
		return
	}

	// Several passes. Loading one driver can make new devices appear behind it.
	//
	// A virtio network card is two steps back: virtio_pci claims the PCI device
	// and creates the virtio bus, and only then does a device matching
	// virtio_net's alias appear. One pass catches the first step and never sees
	// the second. The same goes for every bridge, mux and controller that hangs
	// devices below it.
	//
	// 여러 패스를 돈다. 드라이버 하나를 로드하면 그 뒤로 새 장치가 나타날 수
	// 있다.
	//
	// virtio 네트워크 카드는 두 계단 뒤에 있다: virtio_pci 가 PCI 장치를 잡아
	// virtio 버스를 만들고, 그제서야 virtio_net alias 와 매칭될 장치가
	// 나타난다. 한 패스로는 첫 계단만 잡고 두 번째를 못 본다. 밑에 장치들을
	// 매다는 브리지·먹스·컨트롤러 전부 마찬가지다.
	var loaded []string
	var placed []string
	// A pass that loads nothing is not a reason to stop. On some machines
	// every module matched in the first pass fails (a DS918+ matches only
	// acpi_cpufreq, button and processor, and all three refuse), and stopping
	// there means virtio_net - which appears two steps later - is never even
	// tried, so the machine boots with no network card. Failures are
	// remembered and the passes run to the end.
	//
	// 한 패스에서 하나도 못 올렸다고 멈추면 안 된다. 첫 패스에 매칭되는
	// 것이 전부 실패하는 기계가 있다 (DS918+ 는 acpi_cpufreq/button/
	// processor 셋만 잡히고 셋 다 거부된다). 거기서 끊으면 두 계단 뒤에
	// 나타나는 virtio_net 을 시도조차 못 해 랜카드 없이 부팅된다. 실패한
	// 것만 기억해 두고 패스는 끝까지 돈다.
	failed := map[string]bool{}
	for pass := 1; pass <= 4; pass++ {
		aliases, err := kmod.DeviceAliases(hwscan.DefaultSysfs)
		if err != nil {
			logf("extra drivers: %v", err)
			return
		}
		order := index.ResolvePreferring(aliases, kmod.Loaded("/proc/modules"), originals)
		if len(order) == 0 {
			break
		}
		logf("extra drivers: pass %d: %d candidates", pass, len(order))

		for _, m := range order {
			if failed[m.Name] {
				continue // a failed module is not retried on every pass / 이미 실패한 것을 매 패스마다 다시 시도하지 않는다.
			}
			// request_firmware is called from inside init_module, so the firmware has to
			// be in place before the load. Unpacking it afterwards is already too late.
			//
			// request_firmware 는 init_module 안에서 불린다. 그러니 로드 전에 놓여
			// 있어야 하고, 뒤에 풀면 이미 늦다.
			placed = append(placed, placeFirmware(m, firmware)...)
			if err := kmod.LoadImage(images[m.Name]); err != nil {
				// The error is worth keeping verbatim: a module built for another kernel
				// fails differently from one that failed because no device matched, and only
				// the former is a problem with the pack.
				//
				// 에러 원문을 그대로 남길 가치가 있다. 다른 커널용으로 빌드된 모듈이
				// 실패한 경우와 매칭 장치가 없어서 실패한 경우가 다르게 나타나는데,
				// 팩의 문제는 전자뿐이다.
				logf("extra drivers: %s: %v", m.Name, err)
				failed[m.Name] = true
				continue
			}
			loaded = append(loaded, m.Name)
		}
		// A short pause for the kernel to register what the new drivers found.
		// 새 드라이버가 발견한 것을 커널이 등록할 짧은 여유.
		time.Sleep(500 * time.Millisecond)
	}

	if len(placed) > 0 {
		logf("extra drivers: placed %d firmware files in /lib/firmware", len(placed))
		recordFirmware(firmwareListFile, placed)
	}
	if len(loaded) > 0 {
		logf("extra drivers: loaded %s", strings.Join(loaded, " "))
	} else {
		logf("extra drivers: %d available, none needed", len(index))
	}

	// The per-vendor unlocks, for what alias matching does not reach. All three
	// are cases where the stock kernel driver exists and still fails to claim the
	// device or misbehaves, so it has to be replaced with the pack's or an option
	// has to be set (SR-IOV, invalidating TensorRT). Each function does nothing
	// when its vendor is not present, so there is no side effect on an unrelated
	// system.
	//
	// The NVIDIA one never does anything on the three models supported today -
	// see the header of extradrivers_nvidia.go.
	//
	// alias 로 자동 매칭이 안 되는 벤더별 unlock. 세 벤더 모두 stock 커널
	// 드라이버가 있어도 못 잡거나 오작동하는 케이스라 팩본으로 대체하거나
	// 옵션 (SR-IOV, TensorRT invalidate) 을 걸어야 한다. 각 함수는 벤더가
	// 안 보이면 아무 일도 안 하므로, 관계없는 시스템에서 부작용이 없다.
	//
	// NVIDIA 쪽은 지금 지원하는 세 모델에서는 아무 일도 하지 않는다 -
	// extradrivers_nvidia.go 헤더 참고.
	loadedNames := kmod.Loaded("/proc/modules")
	unlockAquantia(index, images, loadedNames)
	unlockMellanox(index, images, loadedNames)
	unlockNvidia(index, images, loadedNames)
}

// originalsList is the pack entry naming the modules that are Synology's own.
// originalsList - 시놀 자신의 모듈을 적은 팩 항목.
const originalsList = kmod.OriginalsList

// firmwarePrefix tells the firmware entries in a pack apart. An entry carrying
// it is not a module but a file to put under /lib/firmware.
//
// firmwarePrefix - 팩 안에서 펌웨어 항목을 구분하는 접두사. 이게 붙은
// 항목은 모듈이 아니라 /lib/firmware 아래로 놓아야 할 파일이다.
const firmwarePrefix = "firmware/"

// placeFirmware puts only the firmware this module asks for into /lib/firmware.
//
// The whole lot is not unpacked up front because the ramdisk root is RAM. What
// is asked for is written into the .ko's .modinfo as MODULE_FIRMWARE, so
// exactly what will be used can be picked out.
//
// The names carry a subpath ("rtl_nic/rtl8168h-2.fw") and the kernel looks for
// that path as it is, so the directory is created and the name kept.
//
// Failures are passed over quietly: without its firmware that one card does
// not work, but stopping here means not booting at all. The names placed are
// returned, for installFirmware to carry into the installed system.
//
// placeFirmware - 이 모듈이 요구하는 펌웨어만 /lib/firmware 에 놓는다.
//
// 전량을 미리 풀지 않는 이유는 램디스크 루트가 RAM 이기 때문이다. 요구
// 목록은 .ko 의 .modinfo 에 MODULE_FIRMWARE 로 박혀 있어서, 실제로 쓸
// 것만 정확히 고를 수 있다.
//
// 이름에 하위 경로가 들어있고("rtl_nic/rtl8168h-2.fw") 커널이 그 경로
// 그대로 찾으므로 디렉터리를 만들어 이름을 지켜 쓴다.
//
// 실패는 조용히 넘긴다. 펌웨어가 없으면 그 카드만 못 쓰지만, 여기서
// 멈추면 부팅 자체가 안 된다. 놓은 이름을 돌려주어 installFirmware 가 설치된
// 시스템으로 옮기게 한다.
func placeFirmware(m kmod.Module, pack map[string][]byte) []string {
	var placed []string
	for _, want := range m.Firmware {
		data, ok := pack[want]
		if !ok || strings.Contains(want, "..") {
			continue
		}
		dst := path.Join("/lib/firmware", want)
		if _, err := os.Stat(dst); err == nil {
			continue // left alone when the ramdisk already has it / 램디스크가 이미 갖고 있으면 건드리지 않는다.
		}
		if err := os.MkdirAll(path.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			continue
		}
		placed = append(placed, want)
	}
	return placed
}

// firmwareListFile carries the names placeFirmware put under /lib/firmware
// from the stage that loads the drivers to the pivot, like portsFile.
//
// firmwareListFile - placeFirmware 가 /lib/firmware 에 놓은 이름을, 드라이버를
// 올리는 단계에서 pivot 으로 넘긴다 (portsFile 처럼).
const firmwareListFile = "/vibeldr-firmware"

// recordFirmware writes the names to list, one per line.
// recordFirmware - 이름을 list 에 한 줄에 하나씩 쓴다.
func recordFirmware(list string, names []string) {
	if err := os.WriteFile(list, []byte(strings.Join(names, "\n")+"\n"), 0o644); err != nil {
		logf("extra drivers: %s: %v", list, err)
	}
}

// installFirmware copies the firmware the loader placed in the ramdisk into
// the installed system's /lib/firmware.
//
// Many drivers ask for their firmware only when the interface comes up, not
// when the module loads - r8169 does, for one - and by then DSM runs from the
// system on the disk, whose /lib/firmware came from the .pat and holds only
// the firmware DSM ships for its own drivers. A file the installed system
// already has is left alone: it came with DSM. Failures are logged and passed over, as in placeFirmware.
//
// installFirmware - 로더가 램디스크에 놓은 펌웨어를 설치된 시스템의
// /lib/firmware 로 복사한다.
//
// 많은 드라이버가 모듈을 올릴 때가 아니라 인터페이스를 켤 때 펌웨어를 찾는다
// (r8169 가 그렇다). 그때는 DSM 이 디스크의 시스템으로 돌고 있고, 그 /lib/firmware
// 는 .pat 에서 와서 DSM 이 자기 드라이버용으로 싣는 펌웨어만 있다. 설치된 시스템에
// 이미 있는 파일은 DSM 과 함께 온 것이라 건드리지 않는다. 실패는 placeFirmware 처럼 로그만 남기고 넘긴다.
func installFirmware(list, from, root string) int {
	raw, err := os.ReadFile(list)
	if err != nil {
		return 0
	}
	var n int
	for _, name := range strings.Fields(string(raw)) {
		if strings.Contains(name, "..") {
			continue
		}
		dst := path.Join(root, "lib/firmware", name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		data, err := os.ReadFile(path.Join(from, name))
		if err != nil {
			logf("installed system: firmware %s: %v", name, err)
			continue
		}
		if err := os.MkdirAll(path.Dir(dst), 0o755); err != nil {
			logf("installed system: firmware %s: %v", name, err)
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			logf("installed system: firmware %s: %v", name, err)
			continue
		}
		n++
	}
	if n > 0 {
		logf("installed system: placed %d firmware files in %s/lib/firmware", n, root)
	}
	return n
}

// blacklistedModules turns the command line's modprobe.blacklist into a set.
//
// The names are normalised so hyphens and underscores do not matter. The kernel
// treats uhci-hcd and uhci_hcd as the same module, and whichever way it is
// written on the command line it has to be blocked.
//
// blacklistedModules - 커맨드라인의 modprobe.blacklist 를 집합으로 만든다.
//
// 이름은 하이픈·언더스코어를 가리지 않게 맞춰 둔다. 커널은 둘을 같은
// 모듈로 보는데 (uhci-hcd 와 uhci_hcd), 커맨드라인에 어느 쪽으로 적히든
// 차단되어야 한다.
func blacklistedModules() map[string]bool {
	out := map[string]bool{}
	for _, f := range cmdlineFields() {
		name, val, ok := strings.Cut(f, "=")
		if !ok || name != "modprobe.blacklist" {
			continue
		}
		for _, m := range strings.Split(val, ",") {
			if m = strings.TrimSpace(m); m != "" {
				out[strings.ReplaceAll(m, "-", "_")] = true
			}
		}
	}
	return out
}

// loadModuleByName force-loads a module from the pack by name, dependencies
// first.
//
// It follows the pack's dependency table recursively, loading those first and
// then the module itself. loaded (map[name]bool) is what is already in the
// kernel; anything newly loaded is added to that map too, so a second call for
// the same name is a no-op.
//
// It returns (loadedNow, err): (false, nil) when it was already there and
// nothing was loaded, (true, nil) when it went in, and an error when the module
// is not in the pack or the load itself failed.
//
// loadModuleByName - 팩본 모듈을 이름으로 강제 로드한다 (dependency 우선).
//
// 팩 안의 dependency 표를 따라 재귀로 먼저 로드하고, 그 다음 자신을 로드한다.
// loaded (map[name]bool) 은 이미 커널에 들어있는 모듈이다. 새로 로드된 것도
// 이 맵에 갱신하므로, 같은 이름의 두 번째 호출은 no-op 이 된다.
//
// 반환은 (loadedNow, err) 다. 이미 있어서 안 로드했으면 (false, nil),
// 새로 넣었으면 (true, nil), 팩에 없거나 로드 자체가 실패했으면 err 다.
func loadModuleByName(name string, index kmod.Index, images map[string][]byte, loaded map[string]bool) (bool, error) {
	if loaded[name] {
		return false, nil
	}
	m, ok := index[name]
	if !ok {
		return false, fmt.Errorf("kmod: %s not in pack", name)
	}
	// The dependencies are attempted first, best effort. A failure does not stop
	// the module above being loaded. A dependency not in the pack index is
	// skipped without an attempt: some dependencies are already inside the stock
	// kernel as symbols and need no separate load.
	//
	// depends 는 best-effort 로 먼저 시도한다. 실패해도 상위 로드는 진행한다.
	// 팩 인덱스에 없는 depend 는 시도하지 않고 건너뛴다. 어떤 depend 는 stock
	// 커널에 이미 심볼 형태로 포함돼 있어 별도 로드가 필요 없기 때문이다.
	for _, dep := range m.Depends {
		if loaded[dep] {
			continue
		}
		if _, ok := index[dep]; !ok {
			continue
		}
		if _, err := loadModuleByName(dep, index, images, loaded); err != nil {
			logf("extra drivers: %s dep %s: %v", name, dep, err)
		}
	}
	if err := kmod.LoadImage(images[name]); err != nil {
		return false, err
	}
	loaded[name] = true
	return true, nil
}

// packPartition creates the device node for the partition right after the
// payload partition.
//
// This partition cannot be found by name: DSM names only the first three
// partitions of its boot disk and does not even know the fourth exists. It does
// have a number, though, and by the nature of a partition table the one after
// the third is the fourth.
//
// packPartition - payload 파티션 바로 다음 파티션에 대한 device node 를 만든다.
//
// 이 파티션은 이름으로 찾을 수가 없다. DSM 은 자기 부트 디스크의 처음 세
// 파티션에만 이름을 붙이고 네 번째는 존재하는지도 모른다. 대신 번호는 있다.
// 파티션 테이블 특성상 세 번째 다음이 곧 네 번째다.
func packPartition() (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(loaderPayload, &st); err != nil {
		return "", err
	}
	major, minor := deviceNumbers(uint64(st.Rdev))

	_ = os.Remove(packDevice)
	dev := int(mkdev(major, minor+1))
	if err := syscall.Mknod(packDevice, syscall.S_IFBLK|0o600, dev); err != nil {
		return "", fmt.Errorf("%s: %w", packDevice, err)
	}

	// A partition that does not exist still opens, but nothing can be read from
	// it. That looks like an empty archive rather than a missing file, so it is
	// decided explicitly here.
	//
	// 존재하지 않는 파티션은 open 은 되지만 아무것도 못 읽는다. 그러면 없는
	// 파일이 아니라 빈 아카이브처럼 보이므로 여기서 명시적으로 판정한다.
	f, err := os.Open(packDevice)
	if err != nil {
		os.Remove(packDevice)
		return "", err
	}
	defer f.Close()
	if _, err := f.Read(make([]byte, 1)); err != nil {
		os.Remove(packDevice)
		return "", fmt.Errorf("%s is empty", packDevice)
	}
	return packDevice, nil
}

// deviceNumbers splits a device number into major and minor. In the kernel's
// encoding the high bits of each do not sit next to the low ones; they are up
// at the top.
//
// deviceNumbers 는 장치 번호를 major/minor 로 나눈다. 커널 인코딩에서 각
// 필드의 상위 비트는 하위 비트와 붙어 있지 않고 위쪽에 자리한다.
func deviceNumbers(dev uint64) (major, minor uint32) {
	major = uint32((dev>>8)&0xfff) | uint32((dev>>32)&^uint64(0xfff))
	minor = uint32(dev&0xff) | uint32((dev>>12)&^uint64(0xff))
	return
}

func mkdev(major, minor uint32) uint64 {
	return (uint64(major&0xfff) << 8) | (uint64(major) &^ 0xfff << 32) |
		(uint64(minor) & 0xff) | ((uint64(minor) &^ 0xff) << 12)
}
