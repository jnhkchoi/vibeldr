package kpatch

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// analyze_sigenforce.go - the patch sites that make the DSM kernel accept
// unsigned modules.
//
// Background. Flipping the imm8 of the instruction that writes 1 to the
// sig_enforce variable has no effect on this kernel: that is not the real
// enforcement path, and module.sig_enforce=0 on the command line is ignored
// because the parameter is write-once-on.
//
// The place that does work is the "lock" at the start of boot parameter
// handling. During initialisation the kernel sets a few low bits of one global
// flag with successive LOCK ORs, and after that certain parameters can no
// longer be turned off. Changing those ORs into ANDs means the bits are never
// set, the lock never engages, and module.sig_enforce=0 on the command line is
// honoured.
//
// How it is found, with no hard-coded offsets: scan for four consecutive
// instructions of the form LOCK OR byte ptr [rip+disp32], imm8 with imm8 =
// 1, 2, 4 and 8, all targeting the same global address. Requiring the same
// RIP-relative target for all four is what separates a real match from a
// coincidental run of F0 80 bytes.
//
// analyze_sigenforce.go - DSM 커널이 서명 없는 모듈을 받도록 여는 패치 사이트.
//
// 배경:
//
//	sig_enforce 변수에 1 을 쓰는 명령의 imm8 을 0 으로 뒤집어도 이 커널에서는
//	효과가 없다. 그 자리가 실제 강제 경로가 아니고, cmdline 의
//	`module.sig_enforce=0` 은 파라미터가 켜기 전용이라 무시되기 때문이다.
//
//	실제로 먹히는 자리는 부트 파라미터 처리 초입의 "잠금" 이다. 커널은 초기화
//	단계에서 한 전역 플래그의 하위 비트 몇 개를 LOCK OR 로 순차로 세워, 그
//	뒤로는 특정 파라미터를 끄지 못하게 막는다. 이 OR 들을 AND 로 바꾸면 비트가
//	세워지지 않아 잠금이 걸리지 않고, cmdline 의 `module.sig_enforce=0` 이
//	그대로 반영된다.
//
// 찾는 방법 (오프셋 하드코딩 없음):
//
//	같은 전역 주소를 대상으로 하는 `LOCK OR byte ptr [rip+disp32], imm8` 을
//	imm8 = 1, 2, 4, 8 로 연속해서 네 번 하는 자리를 스캔한다. 네 명령의
//	RIP 상대 대상이 모두 같은 주소여야 매치로 인정한다 (우연한 F0 80 나열과
//	구분).

// lockOrInsnLen is the length of F0 80 <modrm> <disp32> <imm8>.
// lockOrInsnLen - `F0 80 <modrm> <disp32> <imm8>` 의 길이.
const lockOrInsnLen = 8

// findBootParamLocks finds the boot parameter lock's LOCK-OR sequence and
// returns the sites that turn each OR into an AND.
//
// On x86, OR r/m8, imm8 is 80 /1 (ModRM.reg = 001) and AND r/m8, imm8 is 80 /4
// (ModRM.reg = 100). Changing only the reg field of the ModRM after the F0
// LOCK prefix, from 001 to 100, leaves the instruction length, its operands
// and its target untouched.
//
// findBootParamLocks - 부트 파라미터 잠금 LOCK-OR 시퀀스를 찾아 각 OR 을
// AND 로 바꾸는 사이트들을 돌려준다.
//
// x86: `OR r/m8, imm8` 은 `80 /1` (ModRM.reg = 001), `AND r/m8, imm8` 은
// `80 /4` (ModRM.reg = 100). LOCK 접두 F0 뒤 ModRM 의 reg 필드만 001 → 100
// 으로 바꾸면 명령 길이·피연산자·타겟이 모두 그대로다.
func findBootParamLocks(v *VMLinux) ([]Site, error) {
	for _, span := range v.TextSpans() {
		// Boot parameter parsing is an __init function, so it lives in
		// .init.text. .text is scanned too; wherever the match turns up is
		// the real place.
		//
		// 부트 파라미터 파싱은 __init 함수라 .init.text 에 있다. .text 도
		// 훑되, 매치가 나오는 곳이 실제 자리다.
		start := int(span.fileOff)
		end := start + int(span.size)
		if end > len(v.Bytes) {
			end = len(v.Bytes)
		}

		locks := scanLockOrRIP(v, start, end)
		if sites := matchLockQuad(v, locks); len(sites) > 0 {
			return sites, nil
		}
	}
	return nil, fmt.Errorf("LOCK-OR quad (imm 1/2/4/8, same target) not found")
}

// lockOr is one LOCK OR [rip+disp32], imm8 instruction.
// lockOr - 하나의 LOCK OR [rip+disp32], imm8 명령.
type lockOr struct {
	fileOff int    // file offset of the F0 that starts it / 명령 시작의 파일 오프셋
	imm     byte   // imm8 / imm8 값
	target  uint64 // virtual address of the RIP-relative target / 대상의 가상 주소
}

// scanLockOrRIP collects every F0 80 0D <disp32> <imm8> in a span.
//
// ModRM 0x0D is mod 00, reg 001 (OR), rm 101 (RIP+disp32), which is the
// encoding of a RIP-relative LOCK OR.
//
// scanLockOrRIP - span 안의 모든 `F0 80 0D <disp32> <imm8>` 을 모은다.
//
// ModRM 0x0D = mod 00, reg 001 (OR), rm 101 (RIP+disp32). 이게 RIP 상대
// LOCK OR 의 인코딩이다.
func scanLockOrRIP(v *VMLinux, start, end int) []lockOr {
	var out []lockOr
	for i := start; i+lockOrInsnLen <= end; i++ {
		if v.Bytes[i] != 0xF0 || v.Bytes[i+1] != 0x80 || v.Bytes[i+2] != 0x0D {
			continue
		}
		disp := int32(binary.LittleEndian.Uint32(v.Bytes[i+3 : i+7]))
		imm := v.Bytes[i+7]
		va, ok := v.FileToVA(uint64(i))
		if !ok {
			continue
		}
		// RIP is the address of the next instruction; a LOCK OR is 8 bytes.
		// RIP 는 다음 명령의 주소. LOCK OR 은 8바이트.
		target := va + uint64(lockOrInsnLen) + uint64(int64(disp))
		out = append(out, lockOr{fileOff: i, imm: imm, target: target})
	}
	return out
}

// findRamdiskCheckJZ bypasses the failure branch of the DSM kernel's ramdisk
// signature check.
//
// When the ramdisk signature fails verification the kernel logs "ramdisk
// corrupt", takes a particular branch, and blocks module loading from there.
// Turning the short JZ on that branch into a JMP makes the failure path pass
// unconditionally. Releasing the boot parameter lock alone
// (findBootParamLocks) still leaves the kernel refusing unsigned modules; both
// patches together are what makes them load.
//
// The algorithm:
//
//  1. Find "3ramdisk corrupt" (16 bytes). The leading 3 is the level digit
//     of the printk prefix: KERN_ERR is "\0013", the SOH byte \001 followed
//     by '3'.
//  2. Take that string's kernel VA minus 1. The original printk argument
//     starts one byte earlier, at the SOH byte.
//  3. Search the whole kernel for the low 32 bits of that VA, little-endian.
//     The kernel loads the string with MOV r64, imm32, and x86-64
//     sign-extends the imm32, so the high 32 bits need not be considered.
//  4. The match position minus 3 must be the start of the instruction: the
//     two bytes before it must be 48 C7 (REX.W + MOV r/m64, imm32) and the
//     third must be C0-C7 (ModRM targeting RAX..RDI).
//  5. Search backwards from there, at most 32 bytes, for 85 C0
//     (TEST EAX, EAX).
//  6. The second byte after the TEST must be 74, a short JZ.
//  7. Return a site that turns that byte into EB, a short JMP. The 8 bit
//     offset is unchanged, so the instruction length and target stay the same.
//
// When several candidates match, the first one that works is used. The string
// does not appear many times in a kernel, but a coincidental byte sequence
// outside .rodata is possible, so the candidates are walked.
//
// findRamdiskCheckJZ - DSM 커널의 램디스크 서명 검증 실패 분기를 우회.
//
// 램디스크 서명이 검증에 실패하면 커널이 "ramdisk corrupt" 로그를 찍고
// 그 뒤 특정 분기로 빠져 모듈 로드를 막는다. 그 분기 판정의 짧은 JZ 를
// JMP 로 바꿔 "실패" 경로를 무조건 통과하게 만든다. 부트파라미터 락
// 해제 (findBootParamLocks) 만으로는 커널이 여전히 서명 없는 모듈을
// 거부하고, 이 두 번째 패치까지 함께 있어야 실제로 로드가 된다.
//
// 알고리즘:
//
//  1. `"3ramdisk corrupt"` (16바이트) 를 찾는다. 앞의 `3` 은 printk 프리픽스의
//     레벨 숫자다. KERN_ERR 는 `"\0013"`, 곧 SOH 바이트 \001 뒤에 '3' 이다.
//  2. 그 문자열의 커널 VA - 1 을 계산한다. 원본 printk 인자는 한 바이트 앞,
//     SOH 바이트에서 시작한다.
//  3. 그 VA 의 하위 32 비트를 리틀엔디언으로 커널 전 영역에서 검색.
//     커널은 이 문자열을 `MOV r64, imm32` 로 로드하는데 x86-64 는 imm32 를
//     sign-extend 하므로 상위 32 비트를 볼 필요가 없다.
//  4. 매치 위치 - 3 이 명령 시작이어야 하고, 앞 두 바이트가 `48 C7`
//     (REX.W + MOV r/m64, imm32), 세 번째 바이트가 C0-C7 (ModRM: RAX..RDI
//     대상) 이어야 한다.
//  5. 그 지점에서 뒤로 최대 32 바이트 안에서 `85 C0` (TEST EAX, EAX) 를
//     찾는다.
//  6. TEST 뒤 두 번째 바이트가 `74` (short JZ) 여야 한다.
//  7. 그 바이트를 `EB` (short JMP) 로 바꾸는 사이트를 돌려준다. 8비트
//     오프셋은 그대로라 명령 길이·타겟이 그대로다.
//
// 매칭이 여럿 나오면 처음 성공하는 후보를 쓴다. 이 문자열이 커널에
// 여러 번 나오지는 않지만 `.rodata` 밖에서 우연히 겹치는 바이트열이
// 있을 수 있어 후보를 순회한다.
func findRamdiskCheckJZ(v *VMLinux) (Site, error) {
	const anchor = "3ramdisk corrupt"

	strOff, ok := v.FindBytes([]byte(anchor))
	if !ok {
		return Site{}, fmt.Errorf("anchor string %q not found", anchor)
	}
	strVA, ok := v.FileToVA(uint64(strOff))
	if !ok {
		return Site{}, fmt.Errorf("anchor at file 0x%x is not mapped", strOff)
	}
	// The original printk argument starts at the SOH byte of the log level
	// prefix, one byte before the '3'.
	//
	// printk 원본 인자는 로그레벨 프리픽스의 SOH 바이트, 곧 '3' 한 바이트
	// 앞에서 시작한다.
	argVA := strVA - 1

	needle := make([]byte, 4)
	binary.LittleEndian.PutUint32(needle, uint32(argVA))

	const backSearch = 32
	for _, hit := range v.FindAllBytes(needle) {
		if hit < 3 {
			continue
		}
		insnOff := hit - 3
		if v.Bytes[insnOff] != 0x48 || v.Bytes[insnOff+1] != 0xC7 {
			continue
		}
		modrm := v.Bytes[insnOff+2]
		if modrm < 0xC0 || modrm > 0xC7 {
			continue
		}
		lower := insnOff - backSearch
		if insnOff < backSearch {
			lower = 0
		}
		for pos := insnOff - 1; pos >= lower; pos-- {
			if pos+2 >= len(v.Bytes) {
				continue
			}
			if v.Bytes[pos] != 0x85 || v.Bytes[pos+1] != 0xC0 {
				continue
			}
			jzOff := pos + 2
			if v.Bytes[jzOff] != 0x74 {
				continue
			}
			return Site{
				Name:     "ramdisk_check_jz",
				FileOff:  uint64(jzOff),
				Old:      0x74,
				New:      0xEB,
				CodeMode: true,
				Expect:   []byte{0x74},
				Data:     []byte{0xEB},
				Why:      "램디스크 서명 검증 우회: TEST eax,eax 뒤 short JZ 를 JMP 로",
			}, nil
		}
	}
	return Site{}, fmt.Errorf("no matching MOV/TEST/JZ around anchor")
}

// matchLockQuad finds four LOCK-ORs with imm 1, 2, 4 and 8 all targeting the
// same address, and turns each OR into an AND site.
//
// The real boot parameter lock sets bits 1/2/4/8 on the same global in
// succession inside one init function, so the four instructions sit close
// together. A kernel can also contain a decoy: the same global touched with
// imm 1/2/4/8 from four different functions, scattered (the DS918+ has one).
// Picking the decoy flips an OR to an AND in unrelated code and breaks the
// boot.
//
// Map iteration order is random, so taking "the first complete quad" would
// alternate between the real one and the decoy from run to run. Instead every
// complete quad candidate is collected and the one whose four instructions are
// packed most tightly - the smallest address span - is chosen
// deterministically, with the lowest address breaking a tie.
//
// matchLockQuad - imm 이 1, 2, 4, 8 이고 대상 주소가 모두 같은 네 개의
// LOCK-OR 를 찾아, 각 OR 을 AND 로 바꾸는 사이트로 만든다.
//
// 진짜 부트 파라미터 잠금은 한 init 함수 안에서 같은 전역에 비트 1/2/4/8 을
// 잇달아 세운다. 즉 네 명령이 좁은 주소 범위에 뭉쳐 있다. 그런데 커널에는
// 우연히 같은 전역을 imm 1/2/4/8 로 건드리는, 서로 다른 함수에 흩어진 가짜
// 조합이 또 있을 수 있다 (DS918+ 가 그렇다). 가짜를 고르면 무관한 코드의
// OR→AND 를 뒤집어 부팅이 깨진다.
//
// map 순회는 순서가 무작위라 "처음 나온 완전한 quad" 를 쓰면 실행마다 진짜와
// 가짜를 오간다. 그래서 완전한 quad 후보를 전부 모은 뒤, 네 명령이 가장 좁게
// 뭉친(주소 span 최소) 것을 결정론적으로 고른다. 동률이면 최소 주소 우선이다.
func matchLockQuad(v *VMLinux, locks []lockOr) []Site {
	// Collect the set of imm values per target address.
	// 대상 주소별로 imm 집합을 모은다.
	byTarget := map[uint64][]lockOr{}
	for _, l := range locks {
		byTarget[l.target] = append(byTarget[l.target], l)
	}

	// Collect every complete quad candidate - one that has all of 1/2/4/8.
	// 완전한 quad (imm 1/2/4/8 을 모두 가진) 후보를 전부 모은다.
	type cand struct {
		target uint64
		want   map[byte]lockOr
		span   uint64 // max minus min fileOff of the four / 네 명령의 fileOff 최대-최소
		lo     uint64 // lowest fileOff, used to break ties / 최소 fileOff (동률 tie-break)
	}
	var cands []cand
	for target, group := range byTarget {
		want := map[byte]lockOr{}
		for _, l := range group {
			switch l.imm {
			case 1, 2, 4, 8:
				if _, dup := want[l.imm]; !dup {
					want[l.imm] = l
				}
			}
		}
		if len(want) != 4 {
			continue
		}
		lo, hi := ^uint64(0), uint64(0)
		for _, l := range want {
			if uint64(l.fileOff) < lo {
				lo = uint64(l.fileOff)
			}
			if uint64(l.fileOff) > hi {
				hi = uint64(l.fileOff)
			}
		}
		cands = append(cands, cand{target: target, want: want, span: hi - lo, lo: lo})
	}
	if len(cands) == 0 {
		return nil
	}
	// Deterministic choice: smallest span, then lowest address.
	// span 최소 → lo 최소 순으로 결정론적 선택.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].span != cands[j].span {
			return cands[i].span < cands[j].span
		}
		return cands[i].lo < cands[j].lo
	})
	want := cands[0].want

	var sites []Site
	for _, imm := range []byte{1, 2, 4, 8} {
		l := want[imm]
		modrmOff := uint64(l.fileOff + 2) // F0 80 [modrm] / F0 80 뒤의 ModRM 위치
		sites = append(sites, Site{
			Name:     fmt.Sprintf("bootparam_lock/or%d", imm),
			FileOff:  modrmOff,
			Old:      0x0D, // OR  (reg=001, rm=101) / OR 인코딩
			New:      0x25, // AND (reg=100, rm=101) / AND 인코딩
			CodeMode: true,
			Expect:   []byte{0x0D},
			Data:     []byte{0x25},
			Why:      "부트 파라미터 잠금 해제: LOCK OR → LOCK AND (module.sig_enforce=0 을 먹히게)",
		})
	}
	return sites
}
