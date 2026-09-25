// event.go is the input event model.
//
// Mouse and keyboard are normalised into one kind of value. Where the input
// actually comes from - a Linux input device or a test script - is none of a
// widget's business.
//
// event.go - 입력 이벤트 모델.
//
// 마우스와 키보드를 한 종류의 값으로 정규화한다. 실제 입력이 어디서
// 오는지 (리눅스 input 장치인지 테스트 스크립트인지) 는 위젯이 알 바가
// 아니다.
package fbui

// EventKind is what kind of event this is.
// EventKind - 이벤트 종류.
type EventKind int

const (
	// EventNone means nothing happened, as when a poll comes back empty.
	// EventNone - 아무 일도 없음. 폴링이 빈손으로 돌아올 때.
	EventNone EventKind = iota
	// EventMouseMove is a cursor move; X and Y are the new position.
	// EventMouseMove - 커서 이동. X, Y 가 새 위치.
	EventMouseMove
	// EventMouseDown and EventMouseUp are the left button going down and up.
	// EventMouseDown, EventMouseUp - 왼쪽 버튼 누름/뗌.
	EventMouseDown
	EventMouseUp
	// EventWheel is the wheel; a positive Delta is upwards.
	// EventWheel - 휠. Delta 가 양수면 위로.
	EventWheel
	// EventKey is a key press: Key for a special key, Rune for a character
	// (0 when there is none).
	//
	// EventKey - 키 누름. Key 가 특수키, Rune 이 문자 (없으면 0).
	EventKey
)

// Key is a special key.
// Key - 특수키.
type Key int

const (
	KeyNone Key = iota
	KeyEnter
	KeyEscape
	KeyTab
	KeyShiftTab
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeySpace
)

// Event is one input.
// Event - 입력 하나.
type Event struct {
	Kind  EventKind
	X, Y  int
	Delta int
	Key   Key
	Rune  rune
}
