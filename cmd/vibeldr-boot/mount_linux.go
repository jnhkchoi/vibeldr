//go:build linux

// mount_linux.go finds the loader's own partitions and mounts them.
//
// The moment vibeldr-boot comes up as PID 1 there is not a single filesystem
// mounted. What it needs is a way to read and write its settings, and that is
// loader partition 1, which the image builder gave a volume label.
// `internal/synoboot.Create` picks the loader disk out by that label, so it is
// reused here as it is.
//
// mount_linux.go - 로더 자신의 파티션을 찾아 붙인다.
//
// vibeldr-boot 이 PID 1 로 뜨는 순간엔 파일시스템이 하나도 안 붙어 있다.
// 설정을 읽고 쓸 창구가 필요한데, 그 창구는 이미지 빌더가 볼륨 라벨을
// 넣어둔 로더 파티션 1 이다. `internal/synoboot.Create` 가 라벨을 보고 로더
// 디스크를 골라내주므로 여기서 그걸 그대로 재사용한다.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"vibeldr/internal/synoboot"
)

// bootLabel is the FAT volume label the image builder puts on partition 1. It
// is the same value as imageLabels[0] in `cmd/vibeldr/main.go`.
//
// bootLabel - 이미지 빌더가 파티션 1 에 심는 FAT 볼륨 라벨.
// (`cmd/vibeldr/main.go` 의 imageLabels[0] 과 같은 값.)
const bootLabel = "VIBELDR1"

// LoaderDisk is what was found out about the loader disk.
// LoaderDisk - 찾아낸 로더 디스크의 정보.
type LoaderDisk struct {
	// Kernel is the disk name the kernel gave it: "sda", "nvme0n1" and so on.
	// Kernel - "sda", "nvme0n1" 같은 커널이 부여한 디스크 이름.
	Kernel string
}

// PartitionDevice is the device path of this disk's nth partition.
//
// The naming differs by bus (`sda3` against `nvme0n1p3`), so using the
// /dev/synoboot<n> built from sysfs is the safe way.
//
// PartitionDevice - 이 디스크의 n 번째 파티션 디바이스 경로.
//
// 이름 규칙이 버스마다 달라서 (`sda3` vs `nvme0n1p3`) sysfs 로 만든
// /dev/synoboot<n> 을 그대로 쓰는 게 안전하다.
func (d LoaderDisk) PartitionDevice(n int) string {
	return fmt.Sprintf("/dev/%s%d", synoboot.Name, n)
}

// findAndCreateLoaderDisk finds the loader disk and creates the /dev/synoboot*
// nodes. A USB loader can still be absent early in the boot, so it retries for
// up to timeout.
//
// findAndCreateLoaderDisk - 로더 디스크를 찾아 /dev/synoboot* 노드를 만든다.
//
// USB 로더는 부트 초반엔 아직 안 나타날 수 있어서 최대 timeout 동안 재시도한다.
func findAndCreateLoaderDisk(timeout time.Duration) (LoaderDisk, error) {
	res, err := synoboot.CreateWithin(synoboot.DefaultSysBlock, "/dev", bootLabel, timeout)
	if err != nil {
		return LoaderDisk{}, err
	}
	return LoaderDisk{Kernel: res.Disk}, nil
}

// mountLoaderConfig mounts loader partition 1 at target as vfat.
//
// vfat comes from the fat and vfat modules loadDrivers loads first
// (bootstrapModules). Without them this call fails; the caller then goes on to
// the menu with a warning that settings will not be saved.
//
// mountLoaderConfig - 로더 파티션 1 을 target 에 vfat 으로 붙인다.
//
// vfat 은 loadDrivers 가 먼저 올리는 fat·vfat 모듈 (bootstrapModules) 에서 온다.
// 그 모듈이 없으면 이 호출이 실패하고, 호출자는 설정이 저장되지 않는다는 경고와
// 함께 메뉴로 넘어간다.
func mountLoaderConfig(disk LoaderDisk, target string) error {
	return mountPartition(disk, 1, target)
}

// mountPartition mounts the loader disk's nth FAT partition at target.
// mountPartition - 로더 디스크의 n 번 FAT 파티션을 target 에 마운트한다.
func mountPartition(disk LoaderDisk, part int, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", target, err)
	}
	dev := disk.PartitionDevice(part)
	// A few names are tried in order: "vfat" first, then "msdos" and "fat"
	// for a kernel that has FAT under another name.
	//
	// 몇 가지 이름을 순서대로 시도한다. "vfat" 을 먼저, 그다음 FAT 을 다른
	// 이름으로 가진 커널을 위해 "msdos" 와 "fat".
	//
	// ENODEV only says the kernel has no filesystem by that name, so it never
	// replaces an error from a name the kernel does know. A disk that refuses
	// writes (EROFS) is mounted read-only instead and counts as mounted: the
	// settings can still be read, saving them reports the error, and
	// synoboot.Check has already said why.
	//
	// ENODEV 는 커널에 그 이름의 파일시스템이 없다는 뜻일 뿐이라, 커널이 아는
	// 이름에서 난 오류를 덮지 않는다. 쓰기를 거부하는 디스크(EROFS)는 대신 읽기
	// 전용으로 붙이고 붙은 것으로 친다. 설정은 읽을 수 있고, 저장하려 하면 오류가
	// 나며, 이유는 synoboot.Check 가 이미 알렸다.
	var last error
	for _, fs := range []string{"vfat", "msdos", "fat"} {
		err := syscall.Mount(dev, target, fs, 0, "iocharset=utf8")
		if err == syscall.EROFS || err == syscall.EACCES {
			if syscall.Mount(dev, target, fs, syscall.MS_RDONLY, "iocharset=utf8") == nil {
				return nil
			}
		}
		if err == nil {
			return nil
		}
		if last == nil || err != syscall.ENODEV {
			last = err
		}
	}
	return fmt.Errorf("mount %s -> %s: %w", dev, target, last)
}

// umountLoaderConfig unmounts target. The return value can be ignored on
// failure; it is clean-up called from syncAll, at the end of a build and right
// before a reboot or a kexec jump.
//
// umountLoaderConfig - target 을 뗀다. 실패해도 반환값은 무시해도 된다
// (syncAll 이 부르는 clean-up 이다. 빌드 끝과 재부팅·kexec 점프 직전에 불린다).
func umountLoaderConfig(target string) error {
	return syscall.Unmount(target, 0)
}

// writePartition writes raw bytes onto the loader disk's nth partition.
//
// p3 is taken out of the loader.img the image builder made and overwritten
// through here. onProgress is called at least once, on completion.
//
// writePartition - 로더 디스크의 n 번 파티션에 raw 바이트를 쓴다.
//
// 이미지 빌더가 만든 loader.img 에서 p3 만 뽑아 이 함수로 덮어쓴다.
// onProgress 는 최소 한 번 (완료 시) 호출된다.
func writePartition(disk LoaderDisk, part int, data []byte, onProgress func(done, total int64)) error {
	dev := disk.PartitionDevice(part)
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", dev, err)
	}
	defer f.Close()

	// The kernel would throw ENOSPC past the end of the partition anyway, but
	// checking first avoids leaving it half written.
	//
	// 파티션 크기를 초과하면 커널이 ENOSPC 로 튕겨내지만, 미리 확인해서
	// 반쯤 쓰인 상태를 피한다.
	if size, ok := blockDeviceSize(disk.Kernel, part); ok && int64(len(data)) > size {
		return fmt.Errorf("payload %d bytes does not fit in %s (%d bytes)", len(data), dev, size)
	}

	const chunk = 1 << 20
	total := int64(len(data))
	var done int64
	for done < total {
		end := done + chunk
		if end > total {
			end = total
		}
		if _, err := f.Write(data[done:end]); err != nil {
			return fmt.Errorf("write %s: %w", dev, err)
		}
		done = end
		if onProgress != nil {
			onProgress(done, total)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dev, err)
	}
	return nil
}

// blockDeviceSize is the partition size in bytes, worked out from the 512-byte
// sector count sysfs reports.
//
// blockDeviceSize - 파티션 크기 (바이트). sysfs 에서 512 바이트 섹터
// 카운트를 읽어 계산한다.
func blockDeviceSize(disk string, part int) (int64, bool) {
	// /sys/class/block/<partition>/size exists whatever the bus. The
	// partition name has several candidates (sd*3, nvme0n1p3 and so on).
	//
	// /sys/class/block/<partition>/size 는 어느 버스든 존재한다.
	// 파티션 이름은 여러 후보를 시도한다 (sd*3, nvme0n1p3 …).
	for _, name := range partitionSysfsNames(disk, part) {
		b, err := os.ReadFile(filepath.Join("/sys/class/block", name, "size"))
		if err != nil {
			continue
		}
		var sectors int64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &sectors); err == nil {
			return sectors * 512, true
		}
	}
	return 0, false
}

// partitionSysfsNames is the sysfs name a partition can go by on this disk.
// partitionSysfsNames - 이 디스크에서 파티션이 가질 수 있는 sysfs 이름.
func partitionSysfsNames(disk string, part int) []string {
	// nvme, mmcblk and loop insert a p: nvme0n1p3, mmcblk0p3, loop0p3.
	// sd, hd and vd do not: sda3.
	//
	// nvme, mmcblk, loop 는 p 를 끼운다: nvme0n1p3, mmcblk0p3, loop0p3
	// sd, hd, vd 는 안 끼운다: sda3
	if strings.HasPrefix(disk, "nvme") || strings.HasPrefix(disk, "mmcblk") ||
		strings.HasPrefix(disk, "loop") {
		return []string{fmt.Sprintf("%sp%d", disk, part)}
	}
	return []string{fmt.Sprintf("%s%d", disk, part)}
}
