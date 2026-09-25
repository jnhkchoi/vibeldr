// microcode.go builds the special initrd the kernel's early-microcode loader
// wants.
//
// An EPYC or a recent Intel CPU can stall early in boot without a microcode
// update. The kernel supports loading microcode early through a very small cpio
// archive placed in front of the real initrd, holding exactly one file:
//
//	kernel/x86/microcode/GenuineIntel.bin  (Intel)
//	kernel/x86/microcode/AuthenticAMD.bin  (AMD)
//
// The content is the vendor's .bin microcode blobs concatenated as they are. It
// must not be compressed: the kernel cannot read it that early.
//
// GRUB concatenates several initrds - initrd A B - and hands them to the kernel
// as one image, so intel-ucode.img and amd-ucode.img are placed on the loader
// partition and listed in grub.cfg in front of the regular ramdisk. The kernel
// picks only the one matching its CPU vendor, so listing both is fine.
//
// microcode.go - 커널 early-microcode 로더용 특수 initrd 생성.
//
// EPYC 이나 최신 인텔 CPU 는 마이크로코드 업데이트가 없으면 부팅 초반에
// 멎어버리는 경우가 있다. 커널은 "정상 initrd 앞에 붙는" 아주 작은 cpio
// 아카이브를 통해 조기 마이크로코드 로딩을 지원한다. 그 안에는 딱 하나의
// 파일이 들어간다:
//
//	kernel/x86/microcode/GenuineIntel.bin  (인텔)
//	kernel/x86/microcode/AuthenticAMD.bin  (AMD)
//
// 파일 내용은 CPU 벤더가 배포하는 .bin 마이크로코드 블롭을 그대로 이어
// 붙인 것이다. 압축하면 커널이 초기 단계에서 인식 못 하므로 반드시
// 비압축이어야 한다.
//
// GRUB 은 `initrd A B` 처럼 두 개 이상의 initrd 를 나열하면 커널에게
// 연결된 하나의 이미지처럼 보이도록 이어붙여서 넘긴다. 그래서 로더
// 파티션에 intel-ucode.img / amd-ucode.img 를 미리 넣어두고 grub.cfg 에서
// 정규 램디스크 앞에 나열한다. 커널은 자기 CPU 벤더에 맞는 것만 골라
// 쓰므로 둘 다 나열해도 된다.
package image

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"vibeldr/internal/ramdisk"
)

// microcodeDir is the path the early-microcode loader has hard-coded.
// microcodeDir - 조기 마이크로코드 로더가 하드코드로 찾는 경로.
const microcodeDir = "kernel/x86/microcode"

// PackIntelUcode concatenates dir/intel-ucode/*.bin and returns the cpio initrd
// bytes for GenuineIntel.
//
// dir is the root of the standard kernel layout, such as the top of a
// linux-firmware tree. A missing intel-ucode/ subdirectory, or one with no .bin
// files, is an error. The concatenation order is the file names sorted
// lexically, which keeps the result reproducible when several files carry the
// same CPU signature.
//
// PackIntelUcode - dir/intel-ucode/*.bin 을 이어붙여 GenuineIntel 용 cpio
// initrd 바이트를 돌려준다.
//
// dir 은 커널 표준 배치의 루트 (예: linux-firmware 트리 최상단) 다. 하위에
// intel-ucode/ 가 없거나 .bin 파일이 없으면 에러다. 이어붙이는 순서는
// 파일 이름 기준 사전순이고, 같은 CPU 서명이 여러 개일 때 재현성을
// 유지하기 위한 것이다.
func PackIntelUcode(dir string) ([]byte, error) {
	data, err := concatBins(filepath.Join(dir, "intel-ucode"), "*.bin")
	if err != nil {
		return nil, fmt.Errorf("pack intel microcode: %w", err)
	}
	return buildMicrocodeCPIO("GenuineIntel", data)
}

// PackAMDUcode concatenates dir/amd-ucode/microcode_amd*.bin and returns the
// cpio initrd bytes for AuthenticAMD.
//
// AMD splits its files by family - microcode_amd.bin,
// microcode_amd_fam15h.bin, microcode_amd_fam17h.bin - and the kernel expects a
// single blob with all of them concatenated.
//
// PackAMDUcode - dir/amd-ucode/microcode_amd*.bin 을 이어붙여
// AuthenticAMD 용 cpio initrd 바이트를 돌려준다.
//
// AMD 는 파일이 microcode_amd.bin, microcode_amd_fam15h.bin,
// microcode_amd_fam17h.bin 처럼 패밀리별로 나뉘어 있어서 커널이 이걸
// 전부 이어붙인 단일 블롭을 기대한다.
func PackAMDUcode(dir string) ([]byte, error) {
	data, err := concatBins(filepath.Join(dir, "amd-ucode"), "microcode_amd*.bin")
	if err != nil {
		return nil, fmt.Errorf("pack amd microcode: %w", err)
	}
	return buildMicrocodeCPIO("AuthenticAMD", data)
}

// buildMicrocodeCPIO builds the cpio bytes for a single name.bin.
// buildMicrocodeCPIO - name.bin 한 개짜리 cpio 바이트를 만든다.
func buildMicrocodeCPIO(name string, data []byte) ([]byte, error) {
	a := ramdisk.NewArchive()
	path := filepath.ToSlash(filepath.Join(microcodeDir, name+".bin"))
	if err := a.Add(path, 0o644, data); err != nil {
		return nil, err
	}
	return a.Bytes()
}

// concatBins opens the files under dir matching pattern in lexical order and
// returns their bytes concatenated.
//
// concatBins - dir 아래 pattern 에 매칭되는 파일들을 사전순으로 열어서
// 바이트를 이어붙여 돌려준다.
func concatBins(dir, pattern string) ([]byte, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, fmt.Errorf("glob %s/%s: %w", dir, pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no %s files under %s", pattern, dir)
	}
	// filepath.Glob's order is platform dependent, so sort explicitly.
	// filepath.Glob 결과 순서는 플랫폼 의존이므로 명시적으로 정렬한다.
	sort.Strings(matches)

	var out []byte
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", m, err)
		}
		out = append(out, b...)
	}
	return out, nil
}
