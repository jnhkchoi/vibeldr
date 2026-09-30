//go:build linux

// keymap_linux.go turns Linux key codes into UI events.
//
// The console has no keymap, so a US layout is carried here. What gets typed on
// a loader screen is a serial number, a MAC address or a number, and one US
// layout covers all of that.
//
// keymap_linux.go - 리눅스 키 코드를 UI 이벤트로 옮긴다.
//
// 콘솔에는 키맵이 없으므로 US 배열을 직접 들고 간다. 로더 화면에서 넣는
// 값은 시리얼 번호, MAC 주소, 숫자 정도라 US 배열 하나로 충분하다.
package fbui

// The Linux key codes, from linux/input-event-codes.h.
// 리눅스 키 코드. linux/input-event-codes.h 의 값.
const (
	keyEsc        = 1
	keyBackspace  = 14
	keyTab        = 15
	keyEnter      = 28
	keyLeftCtrl   = 29
	keyLeftShift  = 42
	keyRightShift = 54
	keyLeftAlt    = 56
	keySpace      = 57
	keyKPEnter    = 96
	keyRightCtrl  = 97
	keyHome       = 102
	keyUp         = 103
	keyPageUp     = 104
	keyLeft       = 105
	keyRight      = 106
	keyEnd        = 107
	keyDown       = 108
	keyPageDown   = 109
	keyDelete     = 111
)

// asciiLower and asciiUpper map a key code to a character, without and with
// shift. A code that is not in the map is not a character key.
//
// asciiLower, asciiUpper - 키 코드를 글자로. 시프트 없을 때와 있을 때다.
// 맵에 없는 코드는 글자 키가 아니다.
var asciiLower = map[uint16]rune{
	2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
	12: '-', 13: '=',
	16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p',
	26: '[', 27: ']',
	30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l',
	39: ';', 40: '\'', 41: '`', 43: '\\',
	44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm',
	51: ',', 52: '.', 53: '/',
	// The numeric keypad. Num Lock is not consulted and these are always
	// taken as digits - nothing on these screens navigates by keypad.
	//
	// 숫자 키패드. 넘버락 상태를 따지지 않고 숫자로 받는다 - 이 화면에서
	// 키패드로 방향 이동을 할 일이 없다.
	71: '7', 72: '8', 73: '9', 75: '4', 76: '5', 77: '6',
	79: '1', 80: '2', 81: '3', 82: '0', 83: '.',
	55: '*', 74: '-', 78: '+',
}

var asciiUpper = map[uint16]rune{
	2: '!', 3: '@', 4: '#', 5: '$', 6: '%', 7: '^', 8: '&', 9: '*', 10: '(', 11: ')',
	12: '_', 13: '+',
	16: 'Q', 17: 'W', 18: 'E', 19: 'R', 20: 'T', 21: 'Y', 22: 'U', 23: 'I', 24: 'O', 25: 'P',
	26: '{', 27: '}',
	30: 'A', 31: 'S', 32: 'D', 33: 'F', 34: 'G', 35: 'H', 36: 'J', 37: 'K', 38: 'L',
	39: ':', 40: '"', 41: '~', 43: '|',
	44: 'Z', 45: 'X', 46: 'C', 47: 'V', 48: 'B', 49: 'N', 50: 'M',
	51: '<', 52: '>', 53: '?',
}

// specialKeys are the keys that are not characters.
// specialKeys - 글자가 아닌 키.
var specialKeys = map[uint16]Key{
	keyEsc:       KeyEscape,
	keyBackspace: KeyBackspace,
	keyTab:       KeyTab,
	keyEnter:     KeyEnter,
	keyKPEnter:   KeyEnter,
	keySpace:     KeySpace,
	keyHome:      KeyHome,
	keyUp:        KeyUp,
	keyPageUp:    KeyPageUp,
	keyLeft:      KeyLeft,
	keyRight:     KeyRight,
	keyEnd:       KeyEnd,
	keyDown:      KeyDown,
	keyPageDown:  KeyPageDown,
	keyDelete:    KeyDelete,
}

// emitKey turns one key code into an event and sends it.
//
// Shift has to be held as state, so it is kept here. Shift itself emits no
// event.
//
// emitKey - 키 코드 하나를 이벤트로 바꿔 내보낸다.
//
// 시프트 눌림은 상태로 들고 있어야 해서 여기서 관리한다. 시프트 자체는
// 이벤트를 내지 않는다.
func (r *InputReader) emitKey(code uint16) {
	switch code {
	case keyLeftShift, keyRightShift:
		r.setShift(true)
		return
	case keyLeftCtrl, keyRightCtrl, keyLeftAlt:
		return
	}

	shift := r.shiftDown()

	if code == keyTab && shift {
		r.emit(Event{Kind: EventKey, Key: KeyShiftTab})
		return
	}
	if k, ok := specialKeys[code]; ok {
		e := Event{Kind: EventKey, Key: k}
		if k == KeySpace {
			e.Rune = ' '
		}
		r.emit(e)
		return
	}

	table := asciiLower
	if shift {
		table = asciiUpper
	}
	if ch, ok := table[code]; ok {
		r.emit(Event{Kind: EventKey, Rune: ch})
	}
}

// releaseKey handles a key going up. It is only there to clear the shift state.
// releaseKey - 키를 뗐을 때. 시프트 상태를 푸는 데만 쓴다.
func (r *InputReader) releaseKey(code uint16) {
	if code == keyLeftShift || code == keyRightShift {
		r.setShift(false)
	}
}

// setShift and shiftDown guard the shift state, which the reader goroutine
// writes and the key translation reads.
//
// setShift / shiftDown - 시프트 상태를 잠금으로 감싼다. 읽기 고루틴이 쓰고
// 키 변환이 읽는다.
func (r *InputReader) setShift(v bool) {
	r.mu.Lock()
	r.shift = v
	r.mu.Unlock()
}

func (r *InputReader) shiftDown() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shift
}
