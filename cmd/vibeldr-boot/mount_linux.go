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
	// Result summarises the /dev/synoboot* nodes synoboot created.
	// Result - synoboot 이 만들어준 /dev/synoboot* 노드 요약.
	Result synoboot.Result
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
	return LoaderDisk{Kernel: res.Disk, Result: res}, nil
}

// mountLoaderConfig mounts loader partition 1 at target as vfat.
//
// vfat is built into the DSM kernel, so the mount needs no module loaded. On a
// kernel where it is not - a self-built one, say - this call fails with EINVAL
// and the caller falls back to the rescue prompt.
//
// mountLoaderConfig - 로더 파티션 1 을 target 에 vfat 으로 붙인다.
//
// vfat 은 DSM 커널에 기본 내장이라 모듈 로드 없이 마운트된다. 커널이 그렇지
// 않은 상황 (자체 빌드 커널 등) 이라면 이 호출이 EINVAL 로 실패하고, 그럴 땐
// 호출자가 rescue 프롬프트로 폴백한다.
func mountLoaderConfig(disk LoaderDisk, target string) error {
	return mountPartition(disk, 1, target)
}

// mountPartition mounts the loader disk's nth partition at target. P1 (the
// settings) and P2 (the embedded DSM original) are opened the same way.
//
// mountPartition - 로더 디스크의 n 번 파티션을 target 에 마운트한다.
// P1(설정), P2(임베드한 DSM 원본) 를 같은 방식으로 연다.
func mountPartition(disk LoaderDisk, part int, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", target, err)
	}
	dev := disk.PartitionDevice(part)
	// A few names are tried in order. A Synology kernel usually knows only
	// "vfat", but some kernels register it as "msdos" or "fat".
	//
	// 몇 가지 이름을 순서대로 시도한다. Synology 커널은 대체로 "vfat" 만
	// 알지만 "msdos" 나 "fat" 로 등록된 커널도 있다.
	var last error
	for _, fs := range []string{"vfat", "msdos", "fat"} {
		if err := syscall.Mount(dev, target, fs, 0, "iocharset=utf8"); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return fmt.Errorf("mount %s -> %s: %w", dev, target, last)
}

// umountLoaderConfig unmounts target. The return value can be ignored on
// failure; it is clean-up called only right before a reboot.
//
// umountLoaderConfig - target 을 뗀다. 실패해도 반환값은 무시해도 된다
// (재부팅 직전에만 부르는 clean-up).
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
	// nvme and mmcblk insert a p: nvme0n1p3, mmcblk0p3.
	// sd, hd and vd do not: sda3.
	//
	// nvme, mmcblk 는 p 를 끼운다: nvme0n1p3, mmcblk0p3
	// sd, hd, vd 는 안 끼운다: sda3
	if strings.HasPrefix(disk, "nvme") || strings.HasPrefix(disk, "mmcblk") ||
		strings.HasPrefix(disk, "loop") {
		return []string{fmt.Sprintf("%sp%d", disk, part)}
	}
	return []string{fmt.Sprintf("%s%d", disk, part)}
}
