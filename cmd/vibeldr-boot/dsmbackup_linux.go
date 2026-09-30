//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"vibeldr/internal/synoboot"
)

// Reading back the identity an installed DSM was booted with.
//
// On every boot the helper leaves a small copy of the loader's settings inside
// the installed system (cmd/vibeldr-init/backup.go): the model, the serial
// number and the MAC addresses the boot used. A new loader for the same
// machine - a reformatted stick, a fresh image - would otherwise make up a new
// serial, and DSM would then run under an identity other than the one it was
// installed and registered with. The wizard reads the copy from the data disks
// and offers its values (buildNetwork, buildIdentity).
//
// DSM's system partition is the first partition of each data disk, an ext4
// file system inside a RAID 1 whose superblock sits at the end, so the
// partition mounts as ext4 on its own. It is mounted read-only with noload,
// which leaves the journal alone as well, so nothing on the disk changes.
//
// 설치된 DSM 이 부팅된 정체성을 다시 읽는다.
//
// 헬퍼는 부팅마다 설치된 시스템 안에 로더 설정의 작은 사본을 남긴다
// (cmd/vibeldr-init/backup.go). 그 부팅이 쓴 모델, 시리얼 번호, MAC 주소다.
// 같은 기계를 위한 새 로더 - 다시 포맷한 USB, 새로 구운 이미지 - 는 그러지
// 않으면 시리얼을 새로 만들고, DSM 은 설치·등록될 때와 다른 정체성으로 돌게
// 된다. 마법사는 데이터 디스크에서 그 사본을 읽어 값을 내놓는다 (buildNetwork,
// buildIdentity).
//
// DSM 의 시스템 파티션은 각 데이터 디스크의 첫 파티션이고, 슈퍼블록이 끝에 있는
// RAID 1 안의 ext4 라 파티션 하나만으로 ext4 로 마운트된다. 읽기 전용에
// noload 로 붙여 저널도 건드리지 않으므로 디스크는 아무것도 바뀌지 않는다.

// dsmBackupManifest is the backup's manifest inside the installed system,
// where backup.go writes it (backupDirRel, backupManifestName).
//
// dsmBackupManifest - 설치된 시스템 안의 백업 매니페스트. backup.go 가 쓰는
// 자리다 (backupDirRel, backupManifestName).
const dsmBackupManifest = "root/.vibeldr/backup/manifest.json"

// dsmBackup is one backup found on a disk.
// dsmBackup - 디스크에서 찾은 백업 하나.
type dsmBackup struct {
	// Disk is where it was read from ("sdb").
	// Disk - 읽어 온 디스크 ("sdb").
	Disk      string
	Model     string   `json:"model"`
	Version   string   `json:"version"`
	Timestamp string   `json:"timestamp"`
	Serial    string   `json:"serial"`
	MACs      []string `json:"macs"`
}

// findDSMBackups reads the backup from every disk but the loader's own, newest
// first. Disks that hold no DSM, or a DSM without the backup, give nothing.
//
// findDSMBackups - 로더 자신의 디스크를 뺀 모든 디스크에서 백업을 읽는다. 새
// 것이 먼저다. DSM 이 없거나 백업이 없는 디스크는 아무것도 내지 않는다.
func findDSMBackups(loader string) []dsmBackup {
	var out []dsmBackup
	for _, p := range dsmPartitions(loader) {
		_ = withDSMSystem(p.Dev, func(root string) error {
			b, err := readDSMBackup(root)
			if err != nil {
				return err
			}
			b.Disk = p.Disk
			out = append(out, b)
			return nil
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	return out
}

// dsmPart is a disk and its partition 1 device ("sdb", "/dev/sdb1"), where an
// installed DSM keeps its system.
//
// dsmPart - 디스크와 그 1 번 파티션 장치 ("sdb", "/dev/sdb1"). 설치된 DSM 이
// 시스템을 두는 자리다.
type dsmPart struct{ Disk, Dev string }

// dsmPartitions are the dsmParts of every disk but the loader's own.
// dsmPartitions - 로더 자신의 디스크를 뺀 모든 디스크의 dsmPart.
func dsmPartitions(loader string) []dsmPart {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	var out []dsmPart
	for _, e := range entries {
		disk := e.Name()
		if disk == loader || !isDataDiskName(disk) {
			continue
		}
		part := firstPartition(disk)
		if part == "" {
			continue
		}
		if dev, err := blockNode(filepath.Join("/sys/block", disk, part), part); err == nil {
			out = append(out, dsmPart{disk, dev})
		}
	}
	return out
}

// blockNode makes sure /dev has a node for the block device whose sysfs
// directory is sysDir and returns its path, /dev/<name>. In this environment
// the partitions of the data disks can be without one, so it is then made from
// the major and minor numbers in sysfs.
//
// blockNode - sysfs 디렉터리가 sysDir 인 블록 장치의 노드가 /dev 에 있게 하고
// 경로 /dev/<name> 을 돌려준다. 이 환경에서는 데이터 디스크의 파티션에 노드가
// 없을 수 있어서, 그럴 때 sysfs 의 major·minor 번호로 하나 만든다.
func blockNode(sysDir, name string) (string, error) {
	dev := "/dev/" + name
	if _, err := os.Stat(dev); err == nil {
		return dev, nil
	}
	raw, err := os.ReadFile(filepath.Join(sysDir, "dev"))
	if err != nil {
		return "", err
	}
	var major, minor uint32
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d:%d", &major, &minor); err != nil {
		return "", err
	}
	if err := synoboot.MknodBlock(dev, major, minor); err != nil {
		return "", err
	}
	return dev, nil
}

// withDSMSystem mounts dev read-only under /mnt, runs fn on the mount point
// and takes the mount down again.
//
// withDSMSystem - dev 를 /mnt 밑에 읽기 전용으로 붙여 그 자리로 fn 을 부르고
// 다시 뗀다.
func withDSMSystem(dev string, fn func(root string) error) error {
	dir := "/mnt/dsm-" + filepath.Base(dev)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defer os.Remove(dir)
	if err := syscall.Mount(dev, dir, "ext4", syscall.MS_RDONLY, "noload"); err != nil {
		return fmt.Errorf("mount %s as ext4: %w", dev, err)
	}
	defer syscall.Unmount(dir, 0)
	return fn(dir)
}

// backupFor is the newest backup made for model that has a serial number.
// backupFor - model 로 만든, 시리얼이 있는 가장 새 백업.
func backupFor(backups []dsmBackup, model string) (dsmBackup, bool) {
	for _, b := range backups {
		if strings.EqualFold(b.Model, model) && b.Serial != "" {
			return b, true
		}
	}
	return dsmBackup{}, false
}

// isDataDiskName leaves out the block devices that are never a DSM disk.
// isDataDiskName - DSM 디스크일 수 없는 블록 장치를 뺀다.
func isDataDiskName(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "sr", "md", "dm-", "fd"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// firstPartition is the name of disk's partition 1 ("sdb1", "nvme0n1p1"),
// found through sysfs so the naming of each bus does not matter.
//
// firstPartition - disk 의 1 번 파티션 이름 ("sdb1", "nvme0n1p1"). sysfs 로
// 찾으므로 버스마다 다른 이름 규칙은 상관없다.
func firstPartition(disk string) string {
	entries, err := os.ReadDir(filepath.Join("/sys/block", disk))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join("/sys/block", disk, e.Name(), "partition"))
		if err == nil && strings.TrimSpace(string(raw)) == "1" {
			return e.Name()
		}
	}
	return ""
}

// readDSMBackup reads the manifest from a mounted system partition.
// readDSMBackup - 붙여 둔 시스템 파티션에서 매니페스트를 읽는다.
func readDSMBackup(root string) (dsmBackup, error) {
	raw, err := os.ReadFile(filepath.Join(root, dsmBackupManifest))
	if err != nil {
		return dsmBackup{}, err
	}
	var b dsmBackup
	if err := json.Unmarshal(raw, &b); err != nil {
		return dsmBackup{}, err
	}
	return b, nil
}
