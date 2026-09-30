package kmod

import (
	"sort"
	"strings"
)

// Pack is one driver pack: file name (r8125.ko) to contents. Besides the
// modules it can hold OriginalsList and firmware, as firmware/<path>.
//
// Pack - 드라이버 팩 하나. 키는 모듈 파일 이름 (`r8125.ko`), 값은 내용.
// 모듈 말고 OriginalsList 와 펌웨어(firmware/<경로>)가 들어 있을 수 있다.
type Pack map[string][]byte

// Names returns the names in the pack, sorted.
// Names - 팩 안의 이름들을 정렬해서 돌려준다.
func (p Pack) Names() []string {
	out := make([]string, 0, len(p))
	for n := range p {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Modules is how many of the entries are modules.
// Modules - 항목 중 모듈의 수.
func (p Pack) Modules() int {
	var n int
	for name := range p {
		if strings.HasSuffix(name, ".ko") {
			n++
		}
	}
	return n
}
