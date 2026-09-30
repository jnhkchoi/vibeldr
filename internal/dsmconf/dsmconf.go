// Package dsmconf edits Synology's key="value" settings files.
//
// DSM keeps its settings in files that look like shell variable assignments
//
//	support_disk_compatibility="yes"
//	maxdisks="12"
//	rss_server="http://update7.synology.com/autoupdate/genRSS.php"
//
// and reads them back with its own get_key_value/set_key_value. Several of the
// things the loader has to change - the disk compatibility check, the update
// servers, the bay count - are single entries in files like these, so changing
// one without disturbing the rest is the whole point of this package.
//
// Order and formatting are preserved. The machine's owner may well read this
// file later, and a rewrite that reorders it makes every subsequent diff
// useless.
//
// Package dsmconf - 시놀로지의 key="value" 설정 파일 편집.
//
// DSM 은 셸 변수 할당처럼 생긴 파일 (위 예) 에 설정을 보관하고, 자체
// get_key_value/set_key_value 로 다시 읽는다. 로더가 손봐야 하는 것들 중
// 몇 개(디스크 호환성 검사, 업데이트 서버, 베이 개수 등)가 이런 파일의 단일
// 엔트리라, 나머지를 건드리지 않고 하나만 바꾸는 게 이 패키지의 목적이다.
//
// 순서와 포맷을 보존한다. 이 파일은 머신 주인이 나중에 볼 수도 있으니,
// 재배치하는 재작성은 이후의 모든 diff 를 쓸모없게 만든다.
package dsmconf

import (
	"fmt"
	"strings"
)

// Set changes one key, adding it at the end if it is not already there.
//
// The value is written quoted, which is how DSM writes its own, and how its
// reader expects to find values containing anything but bare words.
//
// Set - 키 하나를 바꾼다. 없으면 파일 끝에 추가한다. 값은 따옴표로 감싸
// 쓴다. DSM 자신이 그렇게 쓰고, 맨 단어가 아닌 값은 그 형태로 읽는다.
func Set(body, key, value string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		k, _, ok := split(l)
		if !ok || k != key {
			continue
		}
		lines[i] = fmt.Sprintf("%s=%q", key, value)
		return strings.Join(lines, "\n")
	}

	// Not present. Append, keeping exactly one trailing newline whether or not
	// the file already ended with one.
	//
	// 키가 없으면 덧붙인다. 원래 파일이 개행으로 끝났든 아니든 마지막
	// 개행은 정확히 하나만 남긴다.
	trimmed := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	return trimmed + "\n" + fmt.Sprintf("%s=%q", key, value) + "\n"
}

// SetAll applies several keys, in the order given.
// SetAll - 여러 키를 주어진 순서대로 적용한다.
func SetAll(body string, kv [][2]string) string {
	for _, e := range kv {
		body = Set(body, e[0], e[1])
	}
	return body
}

// Get returns the value of a key.
// Get - 키의 값을 돌려준다.
func Get(body, key string) (string, bool) {
	for _, l := range strings.Split(body, "\n") {
		if k, v, ok := split(l); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// split takes one line apart, ignoring comments, blank lines and anything that
// is not an assignment.
//
// split - 한 줄을 키와 값으로 쪼갠다. 주석, 빈 줄, 대입문이 아닌 줄은
// 그냥 건너뛴다.
func split(line string) (key, value string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	value = strings.TrimSpace(s[i+1:])
	// Values are usually quoted, but DSM's own files are not consistent about
	// it and neither is anything that has edited them since.
	//
	// 값은 대개 따옴표로 감싸여 있지만, DSM 자신의 파일도 일관되지 않고
	// 그 뒤에 그 파일을 고친 것들도 마찬가지다.
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return key, value, true
}
