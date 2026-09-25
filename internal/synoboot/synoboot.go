// Package synoboot creates the boot device DSM expects to find.
//
// The DSM updater writes the kernel it just installed back out to the loader,
// and looks for it under one fixed name:
//
//	updater: boot/boot_lock.c(259): failed to mount boot device /dev/synoboot2
//	updater: Failed to mount boot partition
//	updater: Failed to accomplish the update! (errno = 21)
//
// On a real appliance /dev/synoboot is a disk-on-module soldered to the board.
// Anywhere else it does not exist, so the update fails on a missing device
// node - after downloading, verifying and unpacking three gigabytes.
//
// No kernel module is needed. The loader's disk is already there under its own
// name; what is missing is only the name DSM looks for. One syscall per
// partition creates the nodes, and the loader recognises its own disk by the
// volume label the image builder put on the first partition.
//
// Package synoboot - DSM 이 기대하는 부트 장치를 만들어줌.
//
// DSM 업데이터는 자기가 설치한 커널을 로더 쪽으로 다시 쓰고, 정해진
// 이름으로 그걸 찾음:
//
//	updater: boot/boot_lock.c(259): failed to mount boot device /dev/synoboot2
//	updater: Failed to mount boot partition
//	updater: Failed to accomplish the update! (errno = 21)
//
// 실제 어플라이언스에서 /dev/synoboot 은 보드에 납땜된 disk-on-module.
// 다른 데서는 존재하지 않으므로, 3 기가 아카이브 다운로드/검증/전개까지
// 다 끝난 뒤에 device node 없음으로 실패.
//
// 커널 모듈 필요없음. 로더의 디스크는 이미 자기 이름으로 있고, 없는 건
// DSM 이 찾는 이름뿐. 파티션당 syscall 하나로 노드를 만들면 되고,
// 이미지 빌더가 첫 파티션에 심어둔 볼륨 라벨로 로더 디스크가 자기를 인식.
package synoboot

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"vibeldr/internal/hwscan"
)

// Name is the device name the DSM updater will try to mount.
// Name - DSM 업데이터가 마운트할 장치 이름.
const Name = "synoboot"

// DefaultSysBlock is the sysfs path where the kernel lists whole disks.
// DefaultSysBlock - 커널이 전체 디스크를 나열하는 sysfs 경로.
const DefaultSysBlock = "/sys/block"

// virtualPrefixes are block devices that are never the loader: RAM disks, loop
// devices, RAID arrays, device-mapper volumes and optical drives.
//
// virtualPrefixes - 절대 로더가 아닌 블록 장치들: RAM 디스크, loop 장치,
// RAID 어레이, device-mapper 볼륨, 광학 드라이브.
var virtualPrefixes = []string{"loop", "ram", "md", "dm-", "sr", "zram"}

// Result summarises what was created.
// Result - 무엇이 생성됐는지의 요약.
type Result struct {
	// Disk is the kernel's own name for the loader disk, such as "sdb".
	// Disk - 커널이 로더 디스크에 붙인 이름 ("sdb" 등).
	Disk string
	// Created lists the device nodes that now exist.
	// Created - 이제 존재하는 장치 노드 목록.
	Created []string
}

func (r Result) String() string {
	return fmt.Sprintf("%s -> %s", r.Disk, strings.Join(r.Created, " "))
}

// CreateWithin is Create, retried until the loader disk turns up.
//
// A USB loader is not there yet when the boot script first asks: loading the
// driver, scanning the bus and registering the disk all happen after the
// script has gone past. On a machine where the disk is already there the wait
// costs nothing; on one where it is not, it is the difference between working
// and not.
//
// CreateWithin - 로더 디스크가 나타날 때까지 재시도하는 Create.
//
// USB 로더는 부팅 스크립트가 처음 묻는 시점에는 아직 없음. 드라이버 로드,
// 버스 스캔, 디스크 등록이 스크립트가 지나간 뒤에 일어남. 이미 디스크가
// 있는 머신에서는 몇 초 기다림이 비용 없고, 아직 없는 머신에서는 되고
// 안 되고의 차이.
func CreateWithin(sysBlock, devDir, label string, timeout time.Duration) (Result, error) {
	const poll = 250 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		res, err := Create(sysBlock, devDir, label)
		if err == nil || time.Now().After(deadline) {
			return res, err
		}
		time.Sleep(poll)
	}
}

// Create finds the loader disk by the volume label on its first partition and
// gives it the names DSM expects: /dev/synoboot for the disk and
// /dev/synoboot<N> for each partition N it has.
//
// Existing nodes are replaced. One left over from an earlier boot would point
// at whatever disk happened to hold that minor number then.
//
// Create - 첫 파티션의 볼륨 라벨로 로더 디스크를 찾아, DSM 이 기대하는
// 이름을 부여한다: 디스크에는 /dev/synoboot, 파티션 N 마다 /dev/synoboot<N>.
//
// 기존 노드는 교체한다. 이전 부팅에서 남은 스테일 노드는 그때 그 minor
// 번호가 붙은 아무 디스크나 가리키게 될 수 있다.
func Create(sysBlock, devDir, label string) (Result, error) {
	var res Result

	disk, err := findLoaderDisk(sysBlock, devDir, label)
	if err != nil {
		return res, err
	}
	res.Disk = disk

	major, minor, err := readDevNumber(filepath.Join(sysBlock, disk, "dev"))
	if err != nil {
		return res, err
	}
	target := filepath.Join(devDir, Name)
	if err := mknodBlock(target, major, minor); err != nil {
		return res, fmt.Errorf("synoboot: creating %s: %w", target, err)
	}
	res.Created = append(res.Created, target)

	parts, err := partitions(sysBlock, disk)
	if err != nil {
		return res, err
	}
	for _, p := range parts {
		target := fmt.Sprintf("%s%d", filepath.Join(devDir, Name), p.number)
		if err := mknodBlock(target, p.major, p.minor); err != nil {
			return res, fmt.Errorf("synoboot: creating %s: %w", target, err)
		}
		res.Created = append(res.Created, target)
	}
	if len(parts) == 0 {
		return res, fmt.Errorf("synoboot: %s has no partitions", disk)
	}
	return res, nil
}

// findLoaderDisk picks the loader out of the machine's block devices.
// findLoaderDisk - 머신의 블록 장치들 중 로더 디스크를 식별.
func findLoaderDisk(sysBlock, devDir, label string) (string, error) {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return "", fmt.Errorf("synoboot: reading %s: %w", sysBlock, err)
	}
	var looked []string
	for _, e := range entries {
		name := e.Name()
		if isVirtual(name) {
			continue
		}
		looked = append(looked, name)
		got, err := hwscan.FirstPartitionLabel(filepath.Join(devDir, name))
		if err != nil {
			continue
		}
		if strings.EqualFold(got, label) {
			return name, nil
		}
	}
	return "", fmt.Errorf("synoboot: no disk carries the label %q (looked at %s)", label, strings.Join(looked, " "))
}

func isVirtual(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

type partition struct {
	number       int
	major, minor uint32
}

// partitions lists a disk's partitions in partition-number order.
//
// The number comes from the kernel rather than from the directory name,
// because the naming differs by bus (sdb2 against nvme0n1p2).
//
// partitions - 디스크의 파티션을 파티션 번호 순으로 나열.
//
// 번호는 디렉터리 이름이 아니라 커널에서 가져온다. 이름은 버스별로 다르다
// (sdb2 vs nvme0n1p2).
func partitions(sysBlock, disk string) ([]partition, error) {
	dir := filepath.Join(sysBlock, disk)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("synoboot: reading %s: %w", dir, err)
	}
	var out []partition
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), disk) {
			continue
		}
		numRaw, err := os.ReadFile(filepath.Join(dir, e.Name(), "partition"))
		if err != nil {
			continue
		}
		num, err := strconv.Atoi(strings.TrimSpace(string(numRaw)))
		if err != nil {
			continue
		}
		major, minor, err := readDevNumber(filepath.Join(dir, e.Name(), "dev"))
		if err != nil {
			continue
		}
		out = append(out, partition{number: num, major: major, minor: minor})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].number < out[j].number })
	return out, nil
}

// readDevNumber parses a sysfs "dev" file, which holds "major:minor".
// readDevNumber - sysfs 의 "dev" 파일을 파싱 ("major:minor" 형식).
func readDevNumber(path string) (major, minor uint32, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("synoboot: reading %s: %w", path, err)
	}
	maj, min, ok := strings.Cut(strings.TrimSpace(string(b)), ":")
	if !ok {
		return 0, 0, fmt.Errorf("synoboot: %s holds %q, expected major:minor", path, b)
	}
	m, err := strconv.ParseUint(maj, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	n, err := strconv.ParseUint(min, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint32(m), uint32(n), nil
}

// makedev packs a major and minor number the way the mknod syscall expects.
//
// The encoding is not simply major<<8|minor: Linux widened both fields long
// ago and kept the old bits where they were, so the extra bits live further
// up. Small numbers come out the same either way, which is exactly why getting
// this wrong would go unnoticed until it mattered.
//
// makedev - major/minor 를 mknod 시스콜이 기대하는 형태로 묶는다.
//
// 인코딩은 단순한 major<<8|minor 가 아니다. 리눅스가 오래전에 두 필드를
// 넓히면서 기존 비트는 그 자리에 두고 늘어난 비트를 위쪽에 놓았다. 작은
// 번호는 어느 쪽으로 계산해도 같은 값이 나오기 때문에, 틀려도 문제가 될
// 때까지 드러나지 않는다.
func makedev(major, minor uint32) uint64 {
	return (uint64(major&0x00000fff) << 8) |
		(uint64(major&0xfffff000) << 32) |
		(uint64(minor & 0x000000ff)) |
		(uint64(minor&0xffffff00) << 12)
}

// FindDisk names the loader's own disk as the kernel calls it, e.g. "sdb".
//
// Create does the same lookup on its way to making the nodes. It is separate
// here for callers that want the disk without the nodes - the boot log writes
// straight onto the disk and has to work even on a boot where Create never
// succeeded.
//
// FindDisk - 로더 자신의 디스크를 커널이 부르는 이름("sdb" 등) 으로 찾는다.
//
// Create 도 노드를 만드는 길에 같은 조회를 한다. 노드 없이 디스크만 필요한
// 쪽을 위해 따로 뒀다. 부팅 로그는 디스크에 직접 쓰므로, Create 가 성공하지
// 못한 부팅에서도 동작해야 한다.
func FindDisk(sysBlock, devDir, label string) (string, error) {
	return findLoaderDisk(sysBlock, devDir, label)
}
