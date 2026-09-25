// Package cmdline assembles the kernel command line handed to DSM.
//
// Insertion order is preserved, so the same configuration always renders
// byte-identically. Two builds can be diffed directly, and a boot that went
// wrong can be compared against one that did not.
//
// Package cmdline - DSM 에 넘길 커널 커맨드라인 조립.
//
// 삽입 순서를 보존하므로 동일 설정이면 항상 byte-identical 출력이 나온다.
// 두 빌드를 바로 diff 할 수 있고, 정상 부팅 대비 이상 부팅 비교가 쉽다.
package cmdline

import (
	"fmt"
	"sort"
	"strings"
)

// Param is one kernel parameter. An empty Value renders as a bare flag.
// Param - 커널 파라미터 하나. Value 가 빈 문자열이면 flag 로 렌더된다.
type Param struct {
	Key   string
	Value string
}

// Builder accumulates parameters in insertion order.
// Builder - 파라미터를 삽입 순서로 누적한다.
type Builder struct {
	params []Param
	index  map[string]int
}

func New() *Builder {
	return &Builder{index: make(map[string]int)}
}

// Set adds a key=value parameter or replaces an existing value, keeping its
// original position.
//
// Set - key=value 파라미터를 추가하거나 기존 값을 교체한다. 원래 위치는
// 유지한다.
func (b *Builder) Set(key, value string) *Builder {
	if i, ok := b.index[key]; ok {
		b.params[i].Value = value
		return b
	}
	b.index[key] = len(b.params)
	b.params = append(b.params, Param{Key: key, Value: value})
	return b
}

// Add appends a parameter that may appear more than once, leaving earlier
// copies alone.
//
// Most kernel parameters are settings, and giving the same one twice is a
// mistake - which is the assumption Set makes. console= is the documented
// exception: each one names a device to print to, and the last one is what
// userspace inherits as /dev/console. One is the serial port, for capturing a
// log; one is the screen, for somebody watching directly.
//
// Add - 여러 번 나타날 수 있는 파라미터를 추가한다. 이전 사본은 그대로 둔다.
//
// 커널 파라미터 대부분은 설정이고 같은 걸 두 번 지정하면 실수다 (Set 이
// 그 가정을 따른다). console= 는 커널이 문서화한 예외다. 각각이 인쇄할 장치를
// 지정하고, 마지막 것이 유저스페이스가 /dev/console 로 물려받는다.
// 하나는 시리얼 (로그 캡처용), 하나는 화면 (직접 지켜보는 사람용) 이다.
func (b *Builder) Add(key, value string) *Builder {
	b.params = append(b.params, Param{Key: key, Value: value})
	if _, ok := b.index[key]; !ok {
		b.index[key] = len(b.params) - 1
	}
	return b
}

// Flag adds a parameter with no value, such as withefi.
// Flag - 값 없는 파라미터 (예: `withefi`) 추가.
func (b *Builder) Flag(key string) *Builder { return b.Set(key, "") }

// SetIf adds the parameter only when cond is true. It keeps the many
// platform-specific conditionals inside Build readable.
//
// SetIf - cond 가 true 일 때만 파라미터를 추가한다. Build 안의 수많은
// 플랫폼별 조건문을 읽기 좋게 유지하는 용도다.
func (b *Builder) SetIf(cond bool, key, value string) *Builder {
	if cond {
		b.Set(key, value)
	}
	return b
}

func (b *Builder) FlagIf(cond bool, key string) *Builder { return b.SetIf(cond, key, "") }

func (b *Builder) Has(key string) bool { _, ok := b.index[key]; return ok }

func (b *Builder) Get(key string) (string, bool) {
	i, ok := b.index[key]
	if !ok {
		return "", false
	}
	return b.params[i].Value, true
}

// AppendCSV adds a value to a comma-separated parameter, creating it when it is
// not there and skipping a value that already is. It is how the blacklist grows
// without duplicates.
//
// AppendCSV - 콤마 구분 파라미터에 값을 추가한다. 없으면 새로 만들고 이미
// 있으면 건너뛴다. 블랙리스트를 중복 없이 늘리는 데 쓴다.
func (b *Builder) AppendCSV(key, value string) *Builder {
	cur, ok := b.Get(key)
	if !ok || cur == "" {
		return b.Set(key, value)
	}
	for _, part := range strings.Split(cur, ",") {
		if part == value {
			return b
		}
	}
	return b.Set(key, cur+","+value)
}

// Params is an order-preserving copy of the parameter list.
// Params - 파라미터 목록의 순서 유지 사본.
func (b *Builder) Params() []Param {
	out := make([]Param, len(b.params))
	copy(out, b.params)
	return out
}

// String renders the command line as a string.
// String - 커맨드라인 문자열로 렌더한다.
func (b *Builder) String() string {
	parts := make([]string, 0, len(b.params))
	for _, p := range b.params {
		if p.Value == "" {
			parts = append(parts, p.Key)
		} else {
			parts = append(parts, p.Key+"="+p.Value)
		}
	}
	return strings.Join(parts, " ")
}

// Problem is one problem found during validation.
// Problem - 검증에서 발견된 단일 문제.
type Problem struct {
	Key     string
	Message string
}

// Validate rejects a command line that would not boot, or would boot with the
// wrong identity.
//
// Validate - 부팅이 안 되거나 잘못된 identity 로 부팅되는 커맨드라인을
// 거부한다.
func (b *Builder) Validate() []Problem {
	var problems []Problem
	seen := make(map[string]int)

	for _, p := range b.params {
		seen[p.Key]++
		if p.Key == "" {
			problems = append(problems, Problem{Key: "(empty)", Message: "parameter with no name"})
			continue
		}
		if strings.ContainsAny(p.Key, " \t") {
			problems = append(problems, Problem{Key: p.Key, Message: "key contains whitespace"})
		}
		if strings.ContainsAny(p.Value, " \t") {
			problems = append(problems, Problem{Key: p.Key, Message: "value contains whitespace; it would be parsed as a second parameter"})
		}
	}
	for key, n := range seen {
		// console= is the one parameter the kernel is documented to take more
		// than once: each names another device to print to. Everything else
		// repeated is a mistake worth reporting.
		//
		// console= 은 커널이 두 번 이상 받는다고 문서화한 유일한 파라미터다.
		// 각각이 인쇄할 장치를 지정한다. 그 밖의 중복은 보고할 가치가 있는
		// 실수다.
		if key == "console" {
			continue
		}
		if n > 1 {
			problems = append(problems, Problem{Key: key, Message: fmt.Sprintf("appears %d times; the kernel keeps only the last one", n)})
		}
	}

	// Identity parameters DSM requires in order to come up at all.
	// DSM 이 뜨기 위해 반드시 필요한 정체성 파라미터들.
	for _, required := range []string{"syno_hw_version", "sn", "netif_num", "vid", "pid"} {
		if !b.Has(required) {
			problems = append(problems, Problem{Key: required, Message: "required parameter is missing"})
		}
	}

	// netif_num must match the number of macN parameters, or DSM's network
	// setup half-configures itself and the box comes up unreachable.
	//
	// netif_num 은 macN 파라미터 개수와 일치해야 한다. 어긋나면 DSM 의
	// 네트워크 설정이 반쯤만 이루어져 기계에 접속할 수 없게 뜬다.
	if v, ok := b.Get("netif_num"); ok {
		var declared int
		if _, err := fmt.Sscanf(v, "%d", &declared); err != nil {
			problems = append(problems, Problem{Key: "netif_num", Message: "not a number"})
		} else {
			actual := 0
			for i := 1; i <= 8; i++ {
				if b.Has(fmt.Sprintf("mac%d", i)) {
					actual++
				}
			}
			if actual != declared {
				problems = append(problems, Problem{
					Key:     "netif_num",
					Message: fmt.Sprintf("declares %d NIC(s) but %d mac parameter(s) are present", declared, actual),
				})
			}
		}
	}

	if v, ok := b.Get("sn"); ok && len(v) != 13 {
		problems = append(problems, Problem{Key: "sn", Message: fmt.Sprintf("serial has %d characters, expected 13", len(v))})
	}

	sort.Slice(problems, func(i, j int) bool { return problems[i].Key < problems[j].Key })
	return problems
}
