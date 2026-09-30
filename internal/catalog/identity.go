package catalog

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// alphaAlphabet leaves out I and O. Synology never uses either in a serial.
// alphaAlphabet - I 와 O 를 제외. 시놀로지는 시리얼에 이 둘을 절대 안 씀.
const alphaAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ"
const alnumAlphabet = "0123456789ABCDEFGHJKLMNPQRSTUVWXYZ"

func pick(s string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(s))))
	if err != nil {
		return 0, err
	}
	return s[n.Int64()], nil
}

func pickFrom(list []string) (string, error) {
	if len(list) == 0 {
		return "", fmt.Errorf("empty choice list")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	if err != nil {
		return "", err
	}
	return list[n.Int64()], nil
}

// GenerateSerial produces a serial number that satisfies the model's rule.
//
// The layout is always 13 characters: a 4 character prefix, a 3 character
// middle, and a 6 character suffix that is either all digits ("numeric") or
// letter + 4 alphanumerics + letter ("alpha").
//
// GenerateSerial - 모델 규칙에 맞는 시리얼 번호를 만든다.
//
// 배치는 항상 13 자다: 접두 4 자, 가운데 3 자, 그리고 접미 6 자. 접미는
// 전부 숫자("numeric") 이거나 영문자 + 영숫자 4 + 영문자("alpha") 다.
func (c *Catalog) GenerateSerial(model string) (string, error) {
	rule, ok := c.SerialRule(model)
	if !ok {
		return "", fmt.Errorf("no serial rule for model %q", model)
	}
	prefix, err := pickFrom(rule.Prefix)
	if err != nil {
		return "", fmt.Errorf("model %s prefix: %w", model, err)
	}
	middle, err := pickFrom(rule.Middle)
	if err != nil {
		return "", fmt.Errorf("model %s middle: %w", model, err)
	}

	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteString(middle)

	switch strings.ToLower(rule.Suffix) {
	case "numeric":
		n, err := rand.Int(rand.Reader, big.NewInt(30000))
		if err != nil {
			return "", err
		}
		sb.WriteString(fmt.Sprintf("%06d", n.Int64()+1))
	default: // "alpha"
		first, err := pick(alphaAlphabet)
		if err != nil {
			return "", err
		}
		sb.WriteByte(first)
		for i := 0; i < 4; i++ {
			ch, err := pick(alnumAlphabet)
			if err != nil {
				return "", err
			}
			sb.WriteByte(ch)
		}
		last, err := pick(alphaAlphabet)
		if err != nil {
			return "", err
		}
		sb.WriteByte(last)
	}

	serial := sb.String()
	if len(serial) != 13 {
		return "", fmt.Errorf("generated serial %q has length %d, want 13", serial, len(serial))
	}
	return serial, nil
}

var serialPattern = regexp.MustCompile(`^[0-9A-Z]{13}$`)

// ValidateSerial checks a serial against the model's rule. A serial that does
// not match will still boot DSM, but model-locked features (QuickConnect,
// hardware transcoding licences, push notifications) will not activate.
//
// ValidateSerial - 시리얼이 모델 규칙에 맞는지 본다. 안 맞아도 DSM 부팅은
// 되지만, 모델에 묶인 기능 (QuickConnect, 하드웨어 트랜스코딩 라이선스,
// 푸시 알림) 이 활성화되지 않는다.
func (c *Catalog) ValidateSerial(model, serial string) error {
	serial = strings.ToUpper(strings.TrimSpace(serial))
	if !serialPattern.MatchString(serial) {
		return fmt.Errorf("serial must be 13 characters of A-Z and 0-9, got %q", serial)
	}
	rule, ok := c.SerialRule(model)
	if !ok {
		// Unknown model: the length/charset check above is all we can do.
		// 모르는 모델이면 위의 길이·문자 집합 검사가 할 수 있는 전부다.
		return nil
	}
	prefix, middle, suffix := serial[0:4], serial[4:7], serial[7:13]

	if !contains(rule.Prefix, prefix) {
		return fmt.Errorf("prefix %q is not valid for %s (expected one of %s)",
			prefix, model, strings.Join(rule.Prefix, ", "))
	}
	if !contains(rule.Middle, middle) {
		return fmt.Errorf("middle %q is not valid for %s (expected one of %s)",
			middle, model, strings.Join(rule.Middle, ", "))
	}
	switch strings.ToLower(rule.Suffix) {
	case "numeric":
		if _, err := strconv.Atoi(suffix); err != nil {
			return fmt.Errorf("suffix %q must be 6 digits for %s", suffix, model)
		}
	default:
		if !isAlphaSuffix(suffix) {
			return fmt.Errorf("suffix %q must be letter + 4 alphanumerics + letter for %s", suffix, model)
		}
	}
	return nil
}

func isAlphaSuffix(s string) bool {
	if len(s) != 6 {
		return false
	}
	isLetter := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	isAlnum := func(b byte) bool { return isLetter(b) || (b >= '0' && b <= '9') }
	if !isLetter(s[0]) || !isLetter(s[5]) {
		return false
	}
	for i := 1; i < 5; i++ {
		if !isAlnum(s[i]) {
			return false
		}
	}
	return true
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

var macPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// GenerateMACs returns n MAC addresses in the loader's wire format: 12 lower
// case hex digits, no separators. They share one random base and increment, so
// a multi-NIC machine gets a contiguous block the way real hardware does.
//
// GenerateMACs - MAC 주소 n 개를 로더 표기 (구분자 없는 소문자 16진수
// 12 자리) 로 돌려준다. 하나의 무작위 기준값에서 1 씩 올려 만들므로,
// 랜카드가 여럿인 기계는 실제 하드웨어처럼 연속된 블록을 받는다.
func (c *Catalog) GenerateMACs(model string, n int) ([]string, error) {
	if n < 1 || n > 8 {
		return nil, fmt.Errorf("NIC count must be between 1 and 8, got %d", n)
	}
	prefix := DefaultMacPrefix
	if rule, ok := c.SerialRule(model); ok && rule.MacPre != "" {
		prefix = strings.ToLower(rule.MacPre)
	}

	base, err := rand.Int(rand.Reader, big.NewInt(1<<24))
	if err != nil {
		return nil, err
	}
	macs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// Mask back to 24 bits so a base near the top does not overflow into
		// the OUI and hand out an address belonging to another vendor.
		//
		// 24 비트로 다시 자른다. 기준값이 위쪽에 있으면 올림이 OUI 로
		// 넘어가 다른 벤더의 주소를 나눠주게 된다.
		suffix := (base.Int64() + int64(i)) & 0xffffff
		macs = append(macs, fmt.Sprintf("%s%06x", prefix, suffix))
	}
	return macs, nil
}

// NormalizeMAC accepts the usual human formats (colons, dashes, upper case) and
// returns the loader's 12 hex digit form.
//
// NormalizeMAC - 사람이 쓰는 표기 (콜론, 하이픈, 대문자) 를 받아 로더가
// 쓰는 16진수 12 자리 형태로 돌려준다.
func NormalizeMAC(mac string) (string, error) {
	cleaned := strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(mac))
	if !macPattern.MatchString(cleaned) {
		return "", fmt.Errorf("invalid MAC address %q: need 12 hex digits", mac)
	}
	return cleaned, nil
}
