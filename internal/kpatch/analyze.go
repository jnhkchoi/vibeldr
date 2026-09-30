package kpatch

import (
	"fmt"
)

// Site describes one place to patch. There are two shapes:
//
//   - A single byte flip: only Old and New are set and Data is nil. Apply
//     checks that the byte at FileOff is Old and turns it into New.
//   - An in-place code replacement (LOCK-OR to AND, JZ to JMP): Data is
//     non-nil and CodeMode is true. The original cannot look like a data slot,
//     so the slot check is skipped; instead, when Expect is set, the bytes
//     must match it exactly. A mismatch - a different kernel build, say -
//     leaves the site in Skipped.
//
// Site 는 패치 대상 한 곳을 서술한다. 두 가지 모양이 있다.
//
//   - 단일 바이트 flip: Old/New 만 쓰고 Data 는 nil 이다.
//     Apply 는 FileOff 위치의 바이트가 Old 인지 확인하고 New 로 뒤집는다.
//   - 코드 in-place 교체 (LOCK-OR→AND, JZ→JMP): Data non-nil,
//     CodeMode true. 원본이 슬롯 형태일 리 없으므로 슬롯 검증은 건너뛰고,
//     대신 Expect 가 지정돼 있으면 그 바이트열과 정확히 일치하는지 확인한다.
//     일치하지 않으면 (다른 커널 빌드 등) Skipped 로 남긴다.
type Site struct {
	Name    string
	FileOff uint64
	Old     byte
	New     byte
	// Length is the size in bytes of a multi-byte write site.
	// Length 는 다중 바이트 쓰기 사이트에서 쓸 자리 크기 (byte).
	Length int
	// Data is what actually gets written at a multi-byte site.
	// Data 는 다중 바이트 쓰기 사이트에 실제로 쓸 바이트열.
	Data []byte
	// CodeMode true means this site changes bytes inside executable code in
	// place. Apply then checks an exact match against Expect rather than
	// checking for a data-slot shape.
	//
	// CodeMode 가 true 면 이 사이트는 실행 코드 안 바이트를 in-place 로
	// 바꾼다. Apply 는 슬롯 형태 검증 대신 Expect 와의 정확한 일치를 본다.
	CodeMode bool
	// Expect is the original bytes a CodeMode site expects to find. It must be
	// the same length as Data. Empty means overwrite without checking, which is
	// not recommended.
	//
	// Expect 는 CodeMode 사이트에서 쓸 예상 원본 바이트열이다. 길이는 Data 와
	// 같아야 한다. 비어 있으면 원본 확인 없이 덮어쓴다 (권장하지 않음).
	Expect []byte
	// Why is a one-line explanation of why this site is needed. It is logged.
	// Why 는 이 사이트가 왜 필요한지에 대한 한 줄 설명. 로그에 뜬다.
	Why string
}

// Findings is the result of analysing one vmlinux: which sites were found and
// which were not. Something missing is not returned as an error, so that a
// partial patch is still possible.
//
// Findings 는 하나의 vmlinux 분석 결과다. 어떤 사이트를 찾았고, 어떤 건
// 못 찾았는지를 담는다. 못 찾은 게 있어도 부분 패치가 가능하도록 error 를
// 반환하지는 않는다.
type Findings struct {
	Sites   []Site
	Missing []string
}

// Analyze scans a vmlinux for the coordinates that make it load unsigned
// modules:
//
//   - the boot parameter lock, LOCK-OR to AND, so module.sig_enforce=0 takes
//     effect;
//   - the short JZ on the ramdisk signature check's failure branch, turned
//     into a JMP.
//
// The two are independent, so applying only one is still partly useful.
//
// Analyze 는 vmlinux 를 훑어 서명 없는 모듈을 로드하게 만드는 좌표를 모은다.
//
//   - 부트 파라미터 잠금 LOCK-OR → AND (module.sig_enforce=0 을 먹히게)
//   - 램디스크 서명 검증 실패 분기의 short JZ → JMP
//
// 두 사이트는 서로 독립적이라 한쪽만 적용해도 부분적으로 유용하다.
func Analyze(v *VMLinux) (*Findings, error) {
	f := &Findings{}

	// The two places that actually make unsigned modules acceptable. Both have
	// to be opened before the unsigned drivers on the p4 pack will load.
	//
	//	(a) the boot parameter lock, LOCK-OR to AND, so that
	//	    module.sig_enforce=0 on the command line is honoured;
	//	(b) the short JZ on the ramdisk signature check's failure branch,
	//	    turned into a JMP, so the failure path always passes.
	//
	// With only (a) the kernel still says "Loading of unsigned module is
	// rejected". Both are needed.
	//
	// 서명 없는 모듈을 받게 만드는 실제 자리 두 곳. 이 두 곳이 모두 뚫려야
	// p4 팩의 서명 없는 드라이버가 실제로 로드된다.
	//
	//	(a) 부트 파라미터 잠금 LOCK-OR → AND. 커맨드라인의
	//	    module.sig_enforce=0 이 먹히게 한다.
	//	(b) 램디스크 서명 검증 실패 분기의 short JZ → JMP. 실패 경로가 늘
	//	    통과하게 한다.
	//
	// (a) 만 있으면 커널이 여전히 "Loading of unsigned module is rejected" 를
	// 낸다. 둘 다 필요하다.
	if sites, err := findBootParamLocks(v); err == nil {
		f.Sites = append(f.Sites, sites...)
	} else {
		f.Missing = append(f.Missing, fmt.Sprintf("bootparam lock: %v", err))
	}
	if s, err := findRamdiskCheckJZ(v); err == nil {
		f.Sites = append(f.Sites, s)
	} else {
		f.Missing = append(f.Missing, fmt.Sprintf("ramdisk check: %v", err))
	}

	return f, nil
}
