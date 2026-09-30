//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"vibeldr/internal/pat"
)

// The install works in RAM under /work: the .pat, what comes out of it, the
// driver pack and the image. This environment's root is the initramfs, a tmpfs
// the kernel caps at half the RAM. On a 2 GB machine the DS918+ .pat (424 MB)
// and what the extractor unpacks from it (405 MB) do not fit next to the
// environment's own files in that half, and the extractor fails for lack of
// space. So /work gets a tmpfs of its own that may use all the RAM but
// workReserve, and what the build is done with is removed as soon as it is
// (pruneExtracted, removeOriginalsSource).
//
// 설치는 RAM 의 /work 에서 일한다. .pat, 거기서 나온 것, 드라이버 팩, 이미지다.
// 이 환경의 루트는 initramfs 이고, 커널이 RAM 의 절반으로 제한한 tmpfs 다. 2 GB
// 기계에서는 DS918+ .pat (424 MB) 과 추출기가 거기서 푼 것 (405 MB) 이 이 환경
// 자신의 파일과 함께 그 절반에 들어가지 않아, 추출기가 공간 부족으로 실패한다.
// 그래서 /work 에 RAM 에서 workReserve 만 빼고 다 쓸 수 있는 자기 tmpfs 를
// 붙이고, 빌드가 다 쓴 것은 바로 지운다 (pruneExtracted, removeOriginalsSource).

// workReserve is the RAM /work leaves for the kernel and the programs.
// workReserve - /work 가 커널과 프로그램 몫으로 남기는 RAM.
const workReserve = 256 << 20

// mountWorkFS mounts the tmpfs at /work. On failure, or on a machine with no
// more than twice workReserve of RAM, /work stays on the root's own space.
//
// mountWorkFS - /work 에 tmpfs 를 붙인다. 실패하거나 RAM 이 workReserve 의 두 배
// 이하인 기계에서는 /work 가 루트의 공간을 그대로 쓴다.
func mountWorkFS() {
	total := memTotal()
	if total <= 2*workReserve {
		return
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		say("vibeldr-boot: %s: %v", workDir, err)
		return
	}
	size := total - workReserve
	if err := syscall.Mount("tmpfs", workDir, "tmpfs", 0, fmt.Sprintf("size=%d,mode=0755", size)); err != nil {
		say("vibeldr-boot: mount %s: %v", workDir, err)
		return
	}
	say("vibeldr-boot: %s on tmpfs, up to %d MiB", workDir, size>>20)
}

// memTotal is MemTotal from /proc/meminfo in bytes, 0 when unknown.
// memTotal - /proc/meminfo 의 MemTotal (바이트). 모르면 0.
func memTotal() int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}

// pruneExtracted removes whatever the extractor put in dir that the build does
// not use: the updater, firmware images, packages and texts. What stays is
// pat.WantedFiles and hda1.tgz, which mergePatOriginals reads. The downloaded
// .pat itself the caller removes.
//
// pruneExtracted - 추출기가 dir 에 놓은 것 가운데 빌드가 쓰지 않는 것을 지운다.
// 업데이터, 펌웨어 이미지, 패키지, 텍스트다. 남는 것은 pat.WantedFiles 와
// mergePatOriginals 가 읽는 hda1.tgz 다. 내려받은 .pat 자체는 호출자가 지운다.
func pruneExtracted(dir string) {
	keep := map[string]bool{hdaName: true}
	for _, n := range pat.WantedFiles {
		keep[n] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// removeOriginalsSource removes hda1.tgz once its modules are merged.
// removeOriginalsSource - 모듈을 합친 뒤 hda1.tgz 를 지운다.
func removeOriginalsSource(dir string) {
	_ = os.Remove(filepath.Join(dir, hdaName))
}
