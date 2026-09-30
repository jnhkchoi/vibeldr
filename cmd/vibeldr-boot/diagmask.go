package main

import (
	"regexp"
	"strings"
)

// maskedKeys are the loader.yaml values the diagnostics bundle leaves out:
// what identifies the machine, and where and how it sends its notifications.
// Empty values stay as they are.
//
// maskedKeys - 진단 묶음에서 빼는 loader.yaml 값. 기계를 알아볼 수 있는 것,
// 알림을 어디로 어떻게 보내는지. 빈 값은 그대로 둔다.
var maskedKeys = regexp.MustCompile(`^(\s*)(serial|webhook_url|smtp|from|to|username|password):\s*("[^"]+"|'[^']+'|[^\s"'#].*)$`)

// maskedLists are the lists whose items are masked: the MAC addresses, and the
// card order, which names cards by their factory MAC.
//
// maskedLists - 항목을 가리는 목록. MAC 주소, 그리고 카드를 공장 MAC 으로 부르는
// 카드 순서.
var maskedLists = regexp.MustCompile(`^(\s*)(macs|nic_order):\s*(\[.*\])?\s*$`)

// maskConfig blanks the values in maskedKeys and the items of maskedLists.
// maskConfig - maskedKeys 의 값과 maskedLists 의 항목을 지운다.
func maskConfig(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	listIndent := -1
	for i, l := range lines {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if listIndent >= 0 {
			item := strings.TrimSpace(l)
			if strings.HasPrefix(item, "- ") && indent >= listIndent {
				lines[i] = l[:indent] + `- "***"`
				continue
			}
			listIndent = -1
		}
		if m := maskedLists.FindStringSubmatch(l); m != nil {
			if m[3] != "" && m[3] != "[]" {
				lines[i] = m[1] + m[2] + `: ["***"]`
			}
			listIndent = len(m[1])
			continue
		}
		if maskedKeys.MatchString(l) {
			lines[i] = maskedKeys.ReplaceAllString(l, `$1$2: "***"`)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
