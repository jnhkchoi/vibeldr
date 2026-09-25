// widget.go holds the contract every widget shares, and the basic widgets.
//
// A widget draws only inside its own rectangle and handles only the events that
// reach it. Laying the screen out and moving focus around is the Screen's job.
//
// widget.go - 위젯 공통 규약과 기본 위젯.
//
// 위젯은 자기 사각형 안만 그리고, 자기에게 온 이벤트만 처리한다. 화면
// 구성과 포커스 이동은 Screen 이 맡는다.
package fbui

// Screen is the channel a widget uses to ask for something that affects the
// whole screen - opening a popup, moving focus. It is an interface to keep the
// dependency from going in a circle.
//
// Screen - 위젯이 화면 전체에 영향을 주는 일(팝업 띄우기, 포커스 옮기기)
// 을 요청할 때 쓰는 통로. 순환 의존을 피하려고 인터페이스로 둔다.
type Screen interface {
	// OpenPopup registers content for a widget to draw over the others. The
	// dropdown list uses it. Only one can be open at a time.
	//
	// OpenPopup - 위젯이 다른 위젯 위에 겹쳐 그릴 내용을 등록한다.
	// 드롭다운 목록이 이걸 쓴다. 하나만 열려 있을 수 있다.
	OpenPopup(Popup)
	// ClosePopup closes the open popup.
	// ClosePopup - 열린 팝업을 닫는다.
	ClosePopup()
	// Focus moves focus to this widget.
	// Focus - 포커스를 이 위젯으로 옮긴다.
	Focus(Widget)
}

// Popup is content drawn over the other widgets. While it is open it sees
// events first.
//
// Popup - 다른 위젯 위에 겹쳐 그려지는 내용. 열려 있는 동안 이벤트를
// 먼저 받는다.
type Popup interface {
	// PopupBounds is the area drawn over; a click outside it closes the popup.
	// PopupBounds - 겹쳐 그릴 영역. 이 밖을 클릭하면 팝업이 닫힌다.
	PopupBounds() Rect
	// DrawPopup is called last, after every other widget has been drawn.
	// DrawPopup - 다른 위젯을 다 그린 뒤 마지막에 호출된다.
	DrawPopup(*Canvas, *Theme)
	// HandlePopup takes an event while the popup is open; true if consumed.
	// HandlePopup - 팝업이 열려 있는 동안의 이벤트. 소비하면 true.
	HandlePopup(Event, Screen) bool
}

// Widget is a thing placed on the screen.
// Widget - 화면에 놓이는 것.
type Widget interface {
	Bounds() Rect
	SetBounds(Rect)
	Draw(*Canvas, *Theme)
	// Handle takes an event; true if consumed.
	// Handle - 이벤트 처리. 소비했으면 true.
	Handle(Event, Screen) bool
	// CanFocus says whether this is in the tab order. Disabled or
	// display-only widgets are not.
	//
	// CanFocus - 탭 순서에 들어가는지. 비활성이거나 순수 표시용이면 false.
	CanFocus() bool
	// SetFocused records the focus state.
	// SetFocused - 포커스 상태 반영.
	SetFocused(bool)
}

// base is the state every widget shares.
// base - 모든 위젯이 공유하는 상태.
type base struct {
	bounds  Rect
	focused bool
	hovered bool
	pressed bool
	// disabled greys the widget out and stops it seeing events.
	// disabled - 비활성. 회색으로 그려지고 이벤트를 받지 않는다.
	disabled bool
}

// Bounds, SetBounds, CanFocus, SetFocused, Disabled and SetDisabled are the
// parts of Widget that never differ between widgets.
//
// Bounds/SetBounds/CanFocus/SetFocused/Disabled/SetDisabled - 위젯마다
// 달라질 일이 없는 Widget 구현부.
func (b *base) Bounds() Rect      { return b.bounds }
func (b *base) SetBounds(r Rect)  { b.bounds = r }
func (b *base) CanFocus() bool    { return !b.disabled }
func (b *base) SetFocused(v bool) { b.focused = v }
func (b *base) Disabled() bool    { return b.disabled }
func (b *base) SetDisabled(v bool) {
	b.disabled = v
	if v {
		b.focused = false
	}
}

// trackHover updates hovered from a mouse move event. It is the shared step a
// widget calls first.
//
// trackHover - 마우스 이동 이벤트로 hovered 를 갱신한다. 위젯이 가장
// 먼저 부르는 공통 처리.
func (b *base) trackHover(e Event) {
	if e.Kind == EventMouseMove {
		b.hovered = b.bounds.Contains(e.X, e.Y)
	}
}

// Label is the display-only widget that is nothing but text.
// Label - 글자만 있는 표시용 위젯.
type Label struct {
	base
	Text string
	// Align is -1 for left, 0 for centre, 1 for right.
	// Align - -1 왼쪽, 0 가운데, 1 오른쪽.
	Align int
	// Dim draws it in the secondary text colour.
	// Dim - 보조 글자 색으로 그릴지.
	Dim bool
	// Color, when set, is the colour used, taking precedence over Dim.
	// Color - 지정하면 이 색으로 그린다 (Dim 보다 우선).
	Color *Color
	// Font, when set, is the face used; otherwise the body face.
	// Font - 지정하면 이 글꼴로 그린다. 없으면 본문 글꼴.
	Font *Face
}

// NewLabel is a left-aligned label.
// NewLabel - 왼쪽 정렬 기본 라벨.
func NewLabel(text string) *Label { return &Label{Text: text, Align: -1} }

// CanFocus is false; a label takes no input.
// CanFocus - 라벨은 입력을 받지 않는다.
func (l *Label) CanFocus() bool { return false }

// Draw paints the text, truncated to the label's width.
// Draw - 라벨 폭에 맞춰 자른 글자를 그린다.
func (l *Label) Draw(c *Canvas, th *Theme) {
	f := l.Font
	if f == nil {
		f = th.FontBody
	}
	col := th.Text
	if l.Dim {
		col = th.TextDim
	}
	if l.Color != nil {
		col = *l.Color
	}
	c.TextIn(l.bounds, f.Truncate(l.Text, l.bounds.W), col, f, l.Align)
}

// Handle ignores every event.
// Handle - 이벤트를 받지 않는다.
func (l *Label) Handle(Event, Screen) bool { return false }

// Button is the button that sets something off when pressed.
// Button - 눌러서 동작을 일으키는 버튼.
type Button struct {
	base
	Text string
	// OnClick is called the moment the button is released; nil does nothing.
	// OnClick - 버튼을 놓는 순간 호출. nil 이면 아무 일도 안 한다.
	OnClick func()
	// Primary marks the default action (Next, Install) and fills the button
	// with the accent colour.
	//
	// Primary - 기본 동작 버튼 (다음/설치). 강조색으로 채워 그린다.
	Primary bool
}

// NewButton is a button with a label and an action.
// NewButton - 라벨과 동작을 가진 버튼.
func NewButton(text string, onClick func()) *Button {
	return &Button{Text: text, OnClick: onClick}
}

// Draw paints the button in whichever of its states applies.
// Draw - 현재 상태에 맞는 모습으로 버튼을 그린다.
func (b *Button) Draw(c *Canvas, th *Theme) {
	bg, fg, border := th.Control, th.Text, th.Border
	switch {
	case b.disabled:
		bg, fg, border = th.ControlDim, th.TextDim, th.ControlDim
	case b.Primary:
		bg, fg, border = th.Accent, th.TextOnCK, th.Accent
		if b.pressed {
			bg = Blend(th.Accent, Color{}, 40)
		} else if b.hovered {
			bg = Blend(th.Accent, RGB(0xFFFFFF), 30)
		}
	case b.pressed:
		bg = th.ControlActive
	case b.hovered:
		bg = th.ControlHover
	}

	c.FillRound(b.bounds, th.Radius, bg)
	c.BorderRound(b.bounds, th.Radius, 1, border)
	if b.focused && !b.disabled {
		// The focus mark is one more ring inside. Changing only the border
		// colour would be invisible on a Primary button.
		//
		// 포커스 표시는 안쪽에 한 겹 더. 테두리 색만 바꾸면 Primary 버튼
		// 에서 구분이 안 된다.
		c.BorderRound(b.bounds.Inset(3), th.Radius, 1, th.BorderFocus)
	}
	c.TextIn(b.bounds, b.Text, fg, th.FontBody, 0)
}

// Handle takes a press, a release and the keyboard equivalents.
// Handle - 누름·뗌과 그에 해당하는 키 입력을 처리한다.
func (b *Button) Handle(e Event, s Screen) bool {
	if b.disabled {
		return false
	}
	b.trackHover(e)
	switch e.Kind {
	case EventMouseDown:
		if b.bounds.Contains(e.X, e.Y) {
			b.pressed = true
			s.Focus(b)
			return true
		}
	case EventMouseUp:
		if b.pressed {
			b.pressed = false
			if b.bounds.Contains(e.X, e.Y) {
				b.fire()
			}
			return true
		}
	case EventKey:
		// A focused button also fires on Enter or Space. It has to be usable
		// where there is only a keyboard and no mouse.
		//
		// 포커스된 버튼은 엔터·스페이스로도 눌린다. 마우스 없이 키보드만 있는
		// 환경에서도 조작 가능해야 한다.
		if b.focused && (e.Key == KeyEnter || e.Key == KeySpace) {
			b.fire()
			return true
		}
	}
	return false
}

// fire calls OnClick if there is one.
// fire - OnClick 이 있으면 부른다.
func (b *Button) fire() {
	if b.OnClick != nil {
		b.OnClick()
	}
}

// ProgressBar shows progress from 0 to 1.
// ProgressBar - 0..1 진행도.
type ProgressBar struct {
	base
	// Value is between 0 and 1; outside that range it is clamped for drawing.
	// Value - 0 에서 1 사이. 범위를 벗어나면 잘라서 그린다.
	Value float64
	// Text is written over the bar; empty means the percentage.
	// Text - 막대 위에 겹쳐 쓸 글자. 비우면 백분율.
	Text string
	// Indeterminate is for when progress is unknown: stripes rather than a
	// filled bar.
	//
	// Indeterminate - 진행도를 모를 때. 막대를 다 채우지 않고 줄무늬만
	// 그린다.
	Indeterminate bool
}

// CanFocus is false; a progress bar takes no input.
// CanFocus - 진행 막대는 입력을 받지 않는다.
func (p *ProgressBar) CanFocus() bool { return false }

// Draw paints the track, the filled part and the label over it.
// Draw - 바탕과 채운 부분, 그 위의 글자를 그린다.
func (p *ProgressBar) Draw(c *Canvas, th *Theme) {
	radius := p.bounds.H / 2
	c.FillRound(p.bounds, radius, th.ControlDim)
	inner := p.bounds.Inset(2)

	if p.Indeterminate {
		// With no progress to show, evenly spaced blocks go down instead of
		// diagonal stripes. Diagonals are expensive on a framebuffer, and all
		// that matters here is saying "something is happening".
		//
		// 진행도를 모를 때는 일정 간격의 사선 대신 균등한 블록을 깐다.
		// 프레임버퍼에서 사선은 비싸고, 여기서 중요한 건 "진행 중" 표시뿐.
		for x := inner.X; x < inner.X+inner.W; x += 16 {
			c.Fill(Rect{x, inner.Y, 8, inner.H}, Blend(th.ControlDim, th.Accent, 90))
		}
	} else {
		v := p.Value
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		w := int(float64(inner.W) * v)
		if w > 0 {
			c.FillRound(Rect{inner.X, inner.Y, w, inner.H}, inner.H/2, th.Accent)
		}
	}

	label := p.Text
	if label == "" && !p.Indeterminate {
		label = formatPercent(p.Value)
	}
	if label != "" {
		c.TextIn(p.bounds, label, th.Text, th.FontSmall, 0)
	}
}

// Handle ignores every event.
// Handle - 이벤트를 받지 않는다.
func (p *ProgressBar) Handle(Event, Screen) bool { return false }

// formatPercent turns 0..1 into "42%".
// formatPercent - 0..1 을 "42%" 로.
func formatPercent(v float64) string {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	n := int(v*100 + 0.5)
	return itoa(n) + "%"
}

// itoa is a small integer conversion that avoids strconv. This package runs in
// the boot environment, where the dependencies are kept thin.
//
// itoa - strconv 없이 쓰는 작은 정수 변환. 이 패키지는 부팅 환경에서
// 돌아서 의존을 얇게 유지한다.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
