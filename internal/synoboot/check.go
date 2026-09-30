package synoboot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Checking the loader disk before anything is written to it.
//
// When the loader disk cannot be written, nothing says so where it happens.
// The loader's own build fails on partition 3 with a bare "open ... read-only
// file system", and DSM's installer, which mounts partitions 1 and 2 near its
// end, reports the file it just downloaded as damaged. Check looks at the disk
// once, up front, so that the cause can be put in front of the user in plain
// words.
//
// 로더 디스크에 무엇을 쓰기 전에 점검하기.
//
// 로더 디스크에 쓸 수 없을 때 그 자리에서 그렇다고 말해 주는 곳이 없다. 로더의
// 빌드는 파티션 3 에서 "open ... read-only file system" 만 남기고 멈추고, 끝
// 무렵에 파티션 1·2 를 붙이는 DSM 설치기는 방금 받은 파일이 손상됐다고 한다.
// Check 는 처음에 디스크를 한 번 보아, 원인을 사용자에게 알기 쉬운 말로 보여 줄
// 수 있게 한다.

// RequiredPartitions are the partitions the loader disk must have: 1 and 2 are
// mounted by DSM, 3 holds the loader's payload.
//
// RequiredPartitions - 로더 디스크에 꼭 있어야 하는 파티션. 1 과 2 는 DSM 이
// 붙이고, 3 에는 로더의 페이로드가 들어간다.
var RequiredPartitions = []int{1, 2, 3}

// Problem is one thing wrong with the loader disk, worded for the console
// (English, which has a font there) and for the graphical wizard (Korean).
//
// Problem - 로더 디스크의 문제 하나. 콘솔용(글꼴이 있는 영어)과 그래픽
// 마법사용(한글) 문구를 함께 담는다.
type Problem struct {
	English string
	Korean  string
}

// Check reports what is wrong with the loader disk: the kernel marking it
// read-only, a required partition missing, or a partition node that cannot be
// opened for writing. Nothing is written; opening for writing and closing again
// changes nothing on the disk. An empty result means none of these were found.
//
// A read-only mark is cleared first, as the kernel allows for a disk whose own
// switch is off (clearReadOnly). A disk still marked afterwards has a
// write-protect switch on, or the device refuses writes.
//
// Check - 로더 디스크의 문제를 알린다: 커널이 읽기 전용으로 표시했는지, 꼭
// 필요한 파티션이 없는지, 파티션 노드를 쓰기로 열 수 없는지. 아무것도 쓰지
// 않는다. 쓰기로 열었다 닫는 것은 디스크에 아무 변화도 남기지 않는다. 결과가
// 비었으면 이 중 어느 것도 없었다는 뜻이다.
//
// 읽기 전용 표시는 먼저 지워 본다. 장치 자체의 스위치가 꺼져 있으면 커널이 그것을
// 허락한다 (clearReadOnly). 그 뒤에도 표시가 남아 있으면 쓰기 방지 스위치가 켜져
// 있거나 장치가 쓰기를 거부하는 것이다.
func Check(sysBlock, devDir, disk string) []Problem {
	var out []Problem
	if readOnly(filepath.Join(sysBlock, disk, "ro")) {
		_ = clearReadOnly(filepath.Join(devDir, Name))
		if readOnly(filepath.Join(sysBlock, disk, "ro")) {
			out = append(out, Problem{
				English: fmt.Sprintf("the loader disk (%s) is read-only: check for a write-protect switch on the USB stick or card", disk),
				Korean:  fmt.Sprintf("로더 디스크(%s)가 읽기 전용입니다. USB 나 메모리 카드의 쓰기 방지 스위치를 확인하세요.", disk),
			})
			// Every partition fails the checks below for the same reason.
			// 아래 검사는 같은 이유로 모든 파티션에서 실패한다.
			return out
		}
	}

	parts, err := partitions(sysBlock, disk)
	if err != nil {
		return append(out, Problem{
			English: fmt.Sprintf("the loader disk's partitions cannot be read: %v", err),
			Korean:  fmt.Sprintf("로더 디스크의 파티션을 읽을 수 없습니다: %v", err),
		})
	}
	have := map[int]bool{}
	for _, p := range parts {
		have[p.number] = true
	}
	var missing []string
	for _, n := range RequiredPartitions {
		if !have[n] {
			missing = append(missing, fmt.Sprint(n))
		}
	}
	if len(missing) > 0 {
		list := strings.Join(missing, ", ")
		out = append(out, Problem{
			English: fmt.Sprintf("the loader disk (%s) has no partition %s: write the loader image to it again", disk, list),
			Korean:  fmt.Sprintf("로더 디스크(%s)에 파티션 %s 이(가) 없습니다. 로더 이미지를 다시 구우세요.", disk, list),
		})
	}

	for _, n := range RequiredPartitions {
		if !have[n] {
			continue
		}
		node := fmt.Sprintf("%s%d", filepath.Join(devDir, Name), n)
		f, err := os.OpenFile(node, os.O_WRONLY, 0)
		if err != nil {
			out = append(out, Problem{
				English: fmt.Sprintf("loader partition %d cannot be opened for writing: %v", n, err),
				Korean:  fmt.Sprintf("로더 파티션 %d 을 쓰기로 열 수 없습니다: %v", n, err),
			})
			continue
		}
		f.Close()
	}
	return out
}

// readOnly reads a sysfs "ro" file, which holds 1 for a disk the kernel will
// not write to. A file that cannot be read counts as writable, leaving it to
// the open in Check.
//
// readOnly - sysfs 의 "ro" 파일을 읽는다. 커널이 쓰지 않을 디스크면 1 이다.
// 읽을 수 없으면 쓸 수 있는 것으로 치고 Check 의 열기에 맡긴다.
func readOnly(path string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(raw)) == "1"
}
