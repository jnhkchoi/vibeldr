package kpatch

import "fmt"

// ApplyResult is what Apply did: the sites it applied and the ones it skipped.
//
// Apply applies the sites to v, in order. It handles two shapes of site:
//
//   - A single byte flip (Data == nil): check that FileOff holds Old, then
//     turn it into New. A byte that already holds New counts as applied;
//     anything else skips the site and records the reason in Skipped.
//   - A multi-byte write (Data != nil): overwrite
//     [FileOff..FileOff+len(Data)] wholesale. For a CodeMode site the original
//     is checked against Expect (bytes that already equal Data count as
//     applied). Otherwise it is first checked for a plausible slot shape - all
//     printable ASCII, or all zero - so that a misidentified coordinate cannot
//     wipe out unrelated code or data.
//
// ApplyResult 는 Apply 가 한 일, 곧 적용한 사이트와 건너뛴 사이트다.
//
// Apply 는 사이트를 순서대로 v 에 적용한다. 두 가지 사이트 모양을 처리한다:
//
//   - 단일 바이트 flip (Data == nil): FileOff 위치가 Old 인지 확인한 뒤
//     New 로 뒤집는다. 이미 New 이면 적용된 것으로 치고, 그 밖의 값이면 그
//     사이트는 건너뛰고 Skipped 에 이유를 남긴다.
//   - 다중 바이트 쓰기 (Data != nil): [FileOff..FileOff+len(Data)] 를
//     통째로 덮어쓴다. CodeMode 사이트는 원본을 Expect 와 대조한다 (이미
//     Data 와 같으면 적용된 것으로 친다). 그 밖에는 원본이 "그럴싸한 슬롯"
//     (전부 ASCII printable 이거나 전부 0) 인지 먼저 확인해서, 잘못 잡힌
//     좌표에 다른 코드/데이터를 지우는 사고를 막는다.
type ApplyResult struct {
	Applied []Site
	Skipped []SkippedSite
}

// SkippedSite carries a site together with why it was not applied.
// SkippedSite 는 왜 적용되지 않았는지의 이유와 함께 사이트를 담는다.
type SkippedSite struct {
	Site   Site
	Reason string
}

// Apply modifies v.Bytes in place and returns a summary of what happened.
// Apply 는 in-place 로 v.Bytes 를 수정하고 결과 요약을 돌려준다.
func Apply(v *VMLinux, sites []Site) ApplyResult {
	var r ApplyResult
	for _, s := range sites {
		// Multi-byte write. / 다중 바이트 쓰기.
		if s.Data != nil {
			end := s.FileOff + uint64(len(s.Data))
			if end > uint64(len(v.Bytes)) {
				r.Skipped = append(r.Skipped, SkippedSite{
					Site: s, Reason: fmt.Sprintf(
						"오프셋 0x%x..0x%x 가 파일을 벗어남", s.FileOff, end),
				})
				continue
			}
			cur := v.Bytes[s.FileOff:end]
			if s.CodeMode {
				// In-place code replacement: the original cannot be a slot, so
				// Expect is the only check.
				//
				// 코드 in-place 교체: 원본이 슬롯일 리 없으므로 Expect 로만
				// 검증한다.
				if len(s.Expect) != 0 {
					if len(s.Expect) != len(s.Data) {
						r.Skipped = append(r.Skipped, SkippedSite{
							Site: s, Reason: fmt.Sprintf(
								"Expect 길이 %d != Data 길이 %d",
								len(s.Expect), len(s.Data)),
						})
						continue
					}
					if !bytesEqual(cur, s.Expect) {
						// Already overwritten by us, which is harmless, so
						// count it as applied.
						//
						// 이미 우리가 덮어쓴 상태라면 무해하므로 성공으로
						// 간주한다.
						if bytesEqual(cur, s.Data) {
							r.Applied = append(r.Applied, s)
							continue
						}
						r.Skipped = append(r.Skipped, SkippedSite{
							Site: s, Reason: fmt.Sprintf(
								"코드 원본 %d byte 가 예상과 다름 (다른 커널?)",
								len(cur)),
						})
						continue
					}
				}
				copy(cur, s.Data)
				r.Applied = append(r.Applied, s)
				continue
			}
			if !looksLikeSlot(cur) {
				r.Skipped = append(r.Skipped, SkippedSite{
					Site: s, Reason: fmt.Sprintf(
						"현재 %d byte 가 ASCII/0 슬롯 형태가 아님 (좌표 오탐 가능)", len(cur)),
				})
				continue
			}
			copy(cur, s.Data)
			r.Applied = append(r.Applied, s)
			continue
		}

		// Single byte flip. / 단일 바이트 flip.
		if s.FileOff >= uint64(len(v.Bytes)) {
			r.Skipped = append(r.Skipped, SkippedSite{
				Site: s, Reason: fmt.Sprintf("offset 0x%x 가 파일을 벗어남", s.FileOff),
			})
			continue
		}
		got := v.Bytes[s.FileOff]
		if got == s.New {
			// Already applied. Reapplying is harmless; just log it.
			// 이미 적용된 상태. 재적용은 무해하지만 로그만 남긴다.
			r.Applied = append(r.Applied, s)
			continue
		}
		if got != s.Old {
			r.Skipped = append(r.Skipped, SkippedSite{
				Site:   s,
				Reason: fmt.Sprintf("현재 0x%02x, 예상 0x%02x (다른 커널?)", got, s.Old),
			})
			continue
		}
		v.Bytes[s.FileOff] = s.New
		r.Applied = append(r.Applied, s)
	}
	return r
}

// bytesEqual reports whether two byte slices are equal, the same as
// bytes.Equal.
//
// bytesEqual 은 두 바이트열이 같은지 판단한다. bytes.Equal 과 같다.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// looksLikeSlot reports whether the current bytes look like a data slot that is
// safe to overwrite. All zero (a buffer with no value in it yet) or all
// printable ASCII (a default string already in place) passes. A single byte
// that looks like code or a pointer fails it - this is the gate that stops a
// misidentified coordinate from wiping out working code.
//
// looksLikeSlot 은 현재 바이트열이 "덮어써도 되는 데이터 슬롯" 처럼
// 보이는지 판단한다. 전부 0 이거나 (아직 값이 안 실린 버퍼), 전부 ASCII
// printable (기본 문자열이 미리 들어있는 경우) 이면 통과시킨다. 하나라도
// 코드나 포인터로 보이는 바이트가 섞여 있으면 실패시킨다. 좌표를 잘못
// 잡았을 때 멀쩡한 코드를 지우지 않기 위한 관문이다.
func looksLikeSlot(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	allZero := true
	allAscii := true
	for _, c := range b {
		if c != 0 {
			allZero = false
		}
		// Printable ASCII (0x20..0x7e), or a trailing NUL.
		// printable ASCII (0x20..0x7e) 또는 뒤쪽 NUL 종결.
		if !(c == 0 || (c >= 0x20 && c <= 0x7e)) {
			allAscii = false
		}
	}
	return allZero || allAscii
}
