//go:build linux

package main

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"vibeldr/internal/catalog"
	"vibeldr/internal/image"
	"vibeldr/internal/kmod"
)

// Installing without the internet.
//
// Everything the install downloads can come from another USB stick or disk
// instead: the DSM .pat under the name Synology publishes it with
// (DSM_DS918+_90080.pat) and the driver pack under its release name
// (apollolake-DS918+.cpio.gz, with -firmware.cpio.gz and SHA256SUMS beside it),
// at the top of a FAT, exFAT or ext4 partition. The .pat is checked against the
// catalog's MD5 and the pack against SHA256SUMS when it is there, as downloads
// are. The partition is mounted read-only and nothing on it changes.
//
// 인터넷 없이 설치하기.
//
// 설치가 내려받는 것은 모두 다른 USB 나 디스크에서 올 수 있다. 시놀로지가
// 배포하는 이름의 DSM .pat (DSM_DS918+_90080.pat) 과 릴리스 이름의 드라이버 팩
// (apollolake-DS918+.cpio.gz, 옆에 -firmware.cpio.gz 와 SHA256SUMS) 을 FAT,
// exFAT, ext4 파티션 맨 위에 두면 된다. .pat 은 카탈로그의 MD5 로, 팩은 SHA256SUMS 가
// 있으면 그것으로, 받은 것과 똑같이 검사한다. 파티션은 읽기 전용으로 붙이고 그
// 위의 어떤 것도 바꾸지 않는다.

// offlineRoot is where the partition holding the files is mounted.
// offlineRoot - 파일이 있는 파티션을 붙이는 곳.
const offlineRoot = "/mnt/offline"

// offlineSource is a mounted partition with install files on it.
// offlineSource - 설치 파일이 있는 붙여 둔 파티션.
type offlineSource struct {
	Dev string
	// Pat is the .pat's path, empty when this partition has none.
	// Pat - .pat 의 경로. 이 파티션에 없으면 비어 있다.
	Pat string
	// Pack, Firmware and Sums are the driver pack's files, empty when absent.
	// Pack, Firmware, Sums - 드라이버 팩 파일들. 없으면 비어 있다.
	Pack, Firmware, Sums string
}

// findOffline looks through the partitions of every disk but the loader's for
// the .pat at patURL's name and the pack for platform and model, and leaves
// the first partition that has either mounted at offlineRoot. The caller
// unmounts it with closeOffline once the build is done.
//
// findOffline - 로더 디스크를 뺀 모든 디스크의 파티션에서 patURL 이름의 .pat 과
// platform·model 의 팩을 찾고, 둘 중 하나라도 있는 첫 파티션을 offlineRoot 에 붙인
// 채로 둔다. 빌드가 끝나면 호출자가 closeOffline 으로 뗀다.
func findOffline(loader, patURL, platform, model string) (offlineSource, bool) {
	patName := path.Base(patURL)
	packName := catalog.ModulePackName(platform, model)
	fwName := catalog.ModuleFirmwareName(platform, model)
	for _, dev := range offlinePartitions(loader) {
		if err := os.MkdirAll(offlineRoot, 0o755); err != nil {
			return offlineSource{}, false
		}
		mounted := false
		for _, fs := range []string{"vfat", "exfat", "ext4"} {
			opts := ""
			if fs == "ext4" {
				opts = "noload"
			}
			if syscall.Mount(dev, offlineRoot, fs, syscall.MS_RDONLY, opts) == nil {
				mounted = true
				break
			}
		}
		if !mounted {
			continue
		}
		src := offlineSource{Dev: dev}
		entries, _ := os.ReadDir(offlineRoot)
		for _, e := range entries {
			n := e.Name()
			p := filepath.Join(offlineRoot, n)
			switch {
			case samePatName(n, patName):
				src.Pat = p
			case strings.EqualFold(n, packName):
				src.Pack = p
			case strings.EqualFold(n, fwName):
				src.Firmware = p
			case strings.EqualFold(n, catalog.ModuleSums):
				src.Sums = p
			}
		}
		if src.Pat != "" || src.Pack != "" {
			return src, true
		}
		_ = syscall.Unmount(offlineRoot, 0)
	}
	return offlineSource{}, false
}

// closeOffline unmounts what findOffline left mounted.
// closeOffline - findOffline 이 붙여 둔 것을 뗀다.
func closeOffline() {
	_ = syscall.Unmount(offlineRoot, 0)
}

// samePatName compares a file name with the .pat's published one, allowing
// for the "+" a browser may have saved as "%2B".
//
// samePatName - 파일 이름을 배포되는 .pat 이름과 비교한다. 브라우저가 "+" 를
// "%2B" 로 저장했을 수도 있다.
func samePatName(name, want string) bool {
	return strings.EqualFold(strings.ReplaceAll(name, "%2B", "+"), want)
}

// offlinePartitions are the partition devices of every disk but the loader's,
// and a disk with no partitions as a whole.
//
// offlinePartitions - 로더 디스크를 뺀 모든 디스크의 파티션 장치, 그리고
// 파티션이 없는 디스크는 통째로.
func offlinePartitions(loader string) []string {
	disks, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range disks {
		disk := d.Name()
		if disk == loader || !isDataDiskName(disk) {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join("/sys/block", disk))
		var parts []string
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join("/sys/block", disk, e.Name(), "partition")); err == nil {
				parts = append(parts, e.Name())
			}
		}
		if len(parts) == 0 {
			if dev, err := blockNode(filepath.Join("/sys/block", disk), disk); err == nil {
				out = append(out, dev)
			}
			continue
		}
		for _, p := range parts {
			if dev, err := blockNode(filepath.Join("/sys/block", disk, p), p); err == nil {
				out = append(out, dev)
			}
		}
	}
	return out
}

// md5File is a file's MD5 in hex.
// md5File - 파일의 MD5 (16진수).
func md5File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// localDriverPack reads the pack files findOffline found.
// localDriverPack - findOffline 이 찾은 팩 파일을 읽는다.
func localDriverPack(platform, model string, src offlineSource) (kmod.Pack, image.PackInfo, error) {
	packGz, err := os.ReadFile(src.Pack)
	if err != nil {
		return nil, image.PackInfo{}, err
	}
	var fw, sums []byte
	if src.Firmware != "" {
		fw, _ = os.ReadFile(src.Firmware)
	}
	if src.Sums != "" {
		sums, _ = os.ReadFile(src.Sums)
	}
	return image.ModulePackFromFiles(platform, model, packGz, fw, sums)
}
