//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/synoboot"
)

// Opening the loader again from inside DSM.
//
// Changing a setting, or rebuilding after a DSM update, means booting into the
// loader, and without a screen and keyboard on the machine there is no way to
// pick its entry from the boot menu. Run in DSM as root, -next-boot-loader
// writes synoboot.NextBootLoader into synoboot.NextBootFile on the loader's
// partition 1; on the next restart the boot menu opens the loader, which puts
// synoboot.NextBootIdle back, so the boot after that is DSM again. Restarting
// is left to DSM, so the machine goes down the way DSM always takes it down.
//
// DSM 안에서 로더를 다시 열기.
//
// 설정을 바꾸거나 DSM 업데이트 뒤에 다시 빌드하려면 로더로 부팅해야 하는데,
// 기계에 화면과 키보드가 없으면 부팅 메뉴에서 그 항목을 고를 방법이 없다. DSM
// 에서 root 로 -next-boot-loader 를 돌리면 로더 파티션 1 의
// synoboot.NextBootFile 에 synoboot.NextBootLoader 를 쓴다. 다음 재시작 때 부팅
// 메뉴가 로더를 열고, 로더가 synoboot.NextBootIdle 을 되돌려 놓으므로 그다음
// 부팅은 다시 DSM 이다. 재시작은 DSM 에 맡겨, 기계가 DSM 이 늘 내리는 방식으로
// 내려가게 한다.

const (
	// loaderP1Node is the node made for the loader's partition 1, and
	// loaderP1Mount where it is mounted while a file on it is written.
	//
	// loaderP1Node - 로더 파티션 1 용으로 만드는 노드. loaderP1Mount 는 그 위의
	// 파일을 쓰는 동안 붙여 두는 자리.
	loaderP1Node  = hwscan.DefaultDev + "/vibeldr-p1"
	loaderP1Mount = "/tmp/vibeldr-p1"
)

// nextBootLoader writes synoboot.NextBootLoader. The loader disk is the one
// the loader recorded at boot (dsmBootDisk), or, failing that, the one whose
// first partition carries the loader's label.
//
// nextBootLoader - synoboot.NextBootLoader 를 쓴다. 로더 디스크는 로더가 부팅
// 때 적어 둔 것 (dsmBootDisk) 이고, 없으면 첫 파티션에 로더 라벨이 있는
// 디스크다.
func nextBootLoader() error {
	disk := ""
	if raw, err := os.ReadFile(dsmBootDisk); err == nil {
		disk = strings.TrimSpace(string(raw))
	}
	if disk == "" {
		d, err := synoboot.FindDisk(synoboot.DefaultSysBlock, hwscan.DefaultDev, loaderLabel())
		if err != nil {
			return fmt.Errorf("loader disk not found: %w", err)
		}
		disk = d
	}
	dev, err := partitionNodeOf(disk, 1, loaderP1Node)
	if err != nil {
		return err
	}
	defer os.Remove(dev)
	if err := os.MkdirAll(loaderP1Mount, 0o755); err != nil {
		return err
	}
	defer os.Remove(loaderP1Mount)
	loadFAT()
	if err := syscall.Mount(dev, loaderP1Mount, "vfat", 0, ""); err != nil {
		return fmt.Errorf("mount %s (%s partition 1): %w (%s)", dev, disk, err, mountDiag(dev))
	}
	defer syscall.Unmount(loaderP1Mount, 0)

	path := filepath.Join(loaderP1Mount, synoboot.NextBootFile)
	if err := os.WriteFile(path, []byte(synoboot.NextBootLoader), 0o644); err != nil {
		return err
	}
	syscall.Sync()
	return nil
}
