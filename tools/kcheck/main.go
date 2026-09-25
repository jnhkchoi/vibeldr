// kcheck runs the patch analyser over the decrypted DSM kernels in work/dsmcore
// and prints what it found and what it missed, verbatim.
//
// It is for checking the differences between kernel generations without booting
// a VM. The 4.4 and 5.10 kernels give different results from the same analyser,
// and this is where it is settled whether that difference would break a boot.
//
// kcheck - 복호화해 둔 DSM 커널에 패치 분석기를 돌려, 무엇을 찾고
// 무엇을 놓쳤는지 그대로 보여 준다.
//
// VM 을 띄우지 않고 커널 세대별 차이를 확인하려고 쓴다. 4.4 커널과 5.10
// 커널은 같은 분석기로 다른 결과를 내는데, 그 차이가 부팅 실패로 이어지는
// 지를 여기서 먼저 가려낸다.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"vibeldr/internal/kpatch"
)

// main walks work/dsmcore, taking each model directory's zImage apart down to
// its vmlinux and reporting the analysis.
//
// main - work/dsmcore 를 훑으며 모델 디렉터리마다 zImage 를 vmlinux 까지
// 벗겨내고 분석 결과를 찍는다.
func main() {
	root := "work/dsmcore"
	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Println("읽기 실패:", err)
		os.Exit(1)
	}
	var models []string
	for _, e := range entries {
		if e.IsDir() {
			models = append(models, e.Name())
		}
	}
	sort.Strings(models)

	for _, m := range models {
		path := filepath.Join(root, m, "zImage")
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("\n=== %s ===\n  zImage 없음: %v\n", m, err)
			continue
		}
		fmt.Printf("\n=== %s ===\n  zImage %d bytes\n", m, len(raw))

		bz, err := kpatch.ParseBzImage(raw)
		if err != nil {
			fmt.Printf("  bzImage 파싱 실패: %v\n", err)
			continue
		}

		vmBytes, err := bz.ExtractVMLinux()
		if err != nil {
			fmt.Printf("  vmlinux 추출 실패: %v\n", err)
			continue
		}
		fmt.Printf("  vmlinux %d bytes\n", len(vmBytes))

		vm, err := kpatch.ParseVMLinux(vmBytes)
		if err != nil {
			fmt.Printf("  vmlinux 파싱 실패: %v\n", err)
			continue
		}

		f, err := kpatch.Analyze(vm)
		if err != nil {
			fmt.Printf("  분석 실패: %v\n", err)
			continue
		}
		if len(f.Sites) == 0 {
			fmt.Println("  찾은 패치 자리: 없음")
		}
		for _, s := range f.Sites {
			detail := fmt.Sprintf("0x%02x->0x%02x", s.Old, s.New)
			if s.Length > 0 {
				detail = fmt.Sprintf("%d bytes", s.Length)
			}
			fmt.Printf("  [찾음] %-22s off=0x%-8x %s  (%s)\n", s.Name, s.FileOff, detail, s.Why)
		}
		for _, miss := range f.Missing {
			fmt.Printf("  [놓침] %s\n", miss)
		}
	}
}
