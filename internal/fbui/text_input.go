// text_input.go holds the control that is typed into and the radio group that
// picks one of a few choices.
//
// They exist because some values have to be entered by hand rather than
// generated - a serial number, a MAC address. The widget does no validation
// itself; the caller passes one in through Validate, because what counts as
// valid differs from model to model.
//
// text_input.go - 글자를 직접 치는 컨트롤과, 몇 개 중 하나를 고르는 라디오 묶음.
//
// 시리얼 번호나 MAC 주소처럼 자동 생성값을 쓰지 않고 손으로 넣어야 하는
// 값이 있어서 필요하다. 입력값 검증은 위젯이 하지 않고 호출자가 Validate
// 로 끼워 넣는다 - 무엇이 올바른 값인지는 모델마다 다르기 때문.
package fbui

import "strings"

// TextField is a one-line text input.
// TextField - 한 줄 글자 입력.
type TextField struct {
	base
	// text is the current value. It is held as runes so Korean input does not
	// get split apart mid-character.
	//
	// text - 현재 값. 룬 단위로 다뤄야 한글 입력에서 글자가 쪼개지지 않는다.
	text []rune
	// cursor is the position between characters (0..len(text)).
	// cursor - 글자 사이 위치 (0..len(text)).
	cursor int
	// scroll is the rune index display starts at, pushing sideways when the
	// value is longer than the field.
	//
	// scroll - 표시 시작 룬 인덱스. 값이 칸보다 길 때 옆으로 민다.
	scroll int

	// Placeholder is the grey hint shown while the field is empty.
	// Placeholder - 비었을 때 회색으로 보여줄 안내.
	Placeholder string
	// MaxLen is the maximum number of runes; 0 means no limit.
	// MaxLen - 최대 룬 수. 0 이면 제한 없음.
	MaxLen int
	// Filter returns true only for the characters to accept; nil accepts all.
	// Filter - 받아들일 글자만 true. nil 이면 전부 받는다.
	Filter func(rune) bool
	// Upper stores the input upper-cased; a serial number is upper case only.
	// Upper - 입력을 대문자로 바꿔 저장할지. 시리얼은 대문자만 쓴다.
	Upper bool
	// OnChange is called every time the value changes.
	// OnChange - 값이 바뀔 때마다 호출.
	OnChange func(string)
	// Validate says whether the current value is right. An empty string means
	// it is; anything else is shown below the field in red.
	//
	// Validate - 현재 값이 올바른지. 빈 문자열을 돌려주면 정상이고,
	// 아니면 그 메시지를 아래에 빨간 글씨로 보여준다.
	Validate func(string) string
}

// NewTextField is an input field with an initial value.
// NewTextField - 초기값을 가진 입력칸.
func NewTextField(initial string, onChange func(string)) *TextField {
	t := &TextField{OnChange: onChange}
	t.SetText(initial)
	return t
}

// Text is the current value.
// Text - 현재 값.
func (t *TextField) Text() string { return string(t.text) }

// SetText replaces the value, putting the cursor at the end. It does not call
// OnChange.
//
// SetText - 값을 바꾼다. 커서는 끝으로 간다. OnChange 는 부르지 않는다.
func (t *TextField) SetText(s string) {
	t.text = []rune(s)
	if t.MaxLen > 0 && len(t.text) > t.MaxLen {
		t.text = t.text[:t.MaxLen]
	}
	t.cursor = len(t.text)
	t.scroll = 0
}

// Error is the problem Validate reported, or empty when there is none.
// Error - Validate 가 보고한 문제. 없으면 빈 문자열.
func (t *TextField) Error() string {
	if t.Validate == nil {
		return ""
	}
	return t.Validate(t.Text())
}

func (t *TextField) Draw(c *Canvas, th *Theme) {
	bg := th.Control
	if t.disabled {
		bg = th.ControlDim
	}
	c.FillRound(t.bounds, th.Radius, bg)

	border := th.Border
	errMsg := t.Error()
	switch {
	case errMsg != "" && !t.disabled:
		border = th.Danger
	case t.focused:
		border = th.BorderFocus
	}
	c.BorderRound(t.bounds, th.Radius, 1, border)

	inner := Rect{t.bounds.X + 8, t.bounds.Y, t.bounds.W - 16, t.bounds.H}
	old := c.Clip(inner)
	f := th.FontBody
	baseline := inner.Y + (inner.H+f.Ascent()-f.Height()/4)/2

	if len(t.text) == 0 && !t.focused {
		c.TextIn(inner, t.Placeholder, th.TextDim, f, -1)
	} else {
		t.clampScroll(f, inner.W)
		shown := string(t.text[t.scroll:])
		col := th.Text
		if t.disabled {
			col = th.TextDim
		}
		c.Text(inner.X, baseline, shown, col, f)

		// The caret. It does not blink - redrawing is tied to input, so blinking would
		// mean a timer refreshing the screen continuously, and there is no reason to do
		// that to a framebuffer.
		//
		// 커서. 깜빡임은 없다 - 다시 그리는 시점이 입력에 묶여 있어서
		// 깜빡이려면 타이머로 화면을 계속 갱신해야 하고, 프레임버퍼에
		// 그럴 이유가 없다.
		if t.focused && !t.disabled {
			cx := inner.X + f.Measure(string(t.text[t.scroll:t.cursor]))
			c.Fill(Rect{cx, inner.Y + 6, 2, inner.H - 12}, th.Text)
		}
	}
	c.SetClip(old)
}

// clampScroll adjusts where display starts so the cursor stays in view.
// clampScroll - 커서가 보이도록 표시 시작 위치를 맞춘다.
func (t *TextField) clampScroll(f *Face, width int) {
	if t.cursor < t.scroll {
		t.scroll = t.cursor
	}
	for t.scroll < t.cursor && f.Measure(string(t.text[t.scroll:t.cursor])) > width-4 {
		t.scroll++
	}
}

func (t *TextField) Handle(e Event, s Screen) bool {
	if t.disabled {
		return false
	}
	t.trackHover(e)
	switch e.Kind {
	case EventMouseDown:
		if t.bounds.Contains(e.X, e.Y) {
			s.Focus(t)
			return true
		}
	case EventKey:
		if !t.focused {
			return false
		}
		return t.key(e)
	}
	return false
}

func (t *TextField) key(e Event) bool {
	changed := false
	switch e.Key {
	case KeyLeft:
		if t.cursor > 0 {
			t.cursor--
		}
	case KeyRight:
		if t.cursor < len(t.text) {
			t.cursor++
		}
	case KeyHome:
		t.cursor = 0
	case KeyEnd:
		t.cursor = len(t.text)
	case KeyBackspace:
		if t.cursor > 0 {
			t.text = append(t.text[:t.cursor-1], t.text[t.cursor:]...)
			t.cursor--
			changed = true
		}
	case KeyDelete:
		if t.cursor < len(t.text) {
			t.text = append(t.text[:t.cursor], t.text[t.cursor+1:]...)
			changed = true
		}
	default:
		r := e.Rune
		if e.Key == KeySpace && r == 0 {
			r = ' '
		}
		if r == 0 || r < 0x20 {
			return false
		}
		if t.Upper {
			r = []rune(strings.ToUpper(string(r)))[0]
		}
		if t.Filter != nil && !t.Filter(r) {
			// A rejected character still consumes the event, so that a key the filter
			// caught does not leak out as another widget's shortcut.
			//
			// 안 받는 글자도 이벤트는 소비한다. 그래야 필터에 걸린
			// 키가 다른 위젯의 단축키로 새어 나가지 않는다.
			return true
		}
		if t.MaxLen > 0 && len(t.text) >= t.MaxLen {
			return true
		}
		t.text = append(t.text, 0)
		copy(t.text[t.cursor+1:], t.text[t.cursor:])
		t.text[t.cursor] = r
		t.cursor++
		changed = true
	}
	if changed && t.OnChange != nil {
		t.OnChange(t.Text())
	}
	return true
}

// boxSize is one side of the box. It is fixed regardless of the font size, so
// several rows side by side keep their left edge in line.
//
// boxSize - 네모칸 한 변. 글자 크기와 무관하게 고정이라 여러 줄이 나란히
// 놓였을 때 왼쪽 선이 흐트러지지 않는다.
const boxSize = 18

// RadioGroup is a set of rows where exactly one is chosen. Unlike a dropdown
// every item stays visible, which suits a two- or three-way choice such as
// generate automatically against enter by hand.
//
// RadioGroup - 여러 항목 중 하나만 고르는 줄 묶음. 드롭다운과 달리 항목이
// 늘 다 보여서, 두세 개짜리 선택(자동 생성 / 직접 입력)에 쓴다.
type RadioGroup struct {
	base
	// Items are the item labels.
	// Items - 항목 라벨.
	Items []string
	// Selected is the index of the chosen item.
	// Selected - 고른 항목 인덱스.
	Selected int
	// OnChange is called when the choice changes.
	// OnChange - 선택이 바뀌면 호출.
	OnChange func(int)
	// Vertical stacks the items; false lays them out side by side.
	// Vertical - 세로로 쌓을지. false 면 가로로 나란히.
	Vertical bool
	// RowH is one item's height when stacked; 0 means the theme's RowH.
	// RowH - 한 항목의 높이 (세로 배치일 때). 0 이면 테마의 RowH.
	RowH int
}

// NewRadioGroup is a radio group stacked vertically.
// NewRadioGroup - 세로로 쌓는 라디오 묶음.
func NewRadioGroup(items []string, selected int, onChange func(int)) *RadioGroup {
	return &RadioGroup{Items: items, Selected: selected, OnChange: onChange, Vertical: true}
}

// itemRect is the area item i occupies.
// itemRect - i 번째 항목이 차지하는 영역.
func (g *RadioGroup) itemRect(i int, th *Theme) Rect {
	if len(g.Items) == 0 {
		return Rect{}
	}
	if g.Vertical {
		h := g.RowH
		if h == 0 {
			h = th.RowH
		}
		return Rect{g.bounds.X, g.bounds.Y + i*h, g.bounds.W, h}
	}
	w := g.bounds.W / len(g.Items)
	return Rect{g.bounds.X + i*w, g.bounds.Y, w, g.bounds.H}
}

func (g *RadioGroup) Draw(c *Canvas, th *Theme) {
	for i, label := range g.Items {
		r := g.itemRect(i, th)
		dot := Rect{r.X, r.Y + (r.H-boxSize)/2, boxSize, boxSize}

		fg, border := th.Text, th.Border
		if g.disabled {
			fg, border = th.TextDim, th.ControlDim
		} else if g.focused && i == g.Selected {
			border = th.BorderFocus
		}

		// Drawn as a square with its corners cut rather than as a circle. A small
		// circle drawn straight onto a framebuffer stairsteps badly and looks worse.
		//
		// 동그라미 대신 모서리를 깎은 네모로 그린다. 작은 원을 프레임버퍼
		// 에 직접 그리면 계단이 심해서 오히려 지저분하다.
		c.Fill(dot.Inset(1), th.Control)
		c.Border(dot, 1, border)
		c.Set(dot.X, dot.Y, th.Control)
		c.Set(dot.X+dot.W-1, dot.Y, th.Control)
		c.Set(dot.X, dot.Y+dot.H-1, th.Control)
		c.Set(dot.X+dot.W-1, dot.Y+dot.H-1, th.Control)

		if i == g.Selected {
			mark := th.Accent
			if g.disabled {
				mark = th.TextDim
			}
			c.Fill(dot.Inset(5), mark)
		}

		lr := Rect{dot.X + boxSize + 10, r.Y, r.W - boxSize - 10, r.H}
		c.TextIn(lr, th.FontBody.Truncate(label, lr.W), fg, th.FontBody, -1)
	}
}

func (g *RadioGroup) Handle(e Event, s Screen) bool {
	if g.disabled {
		return false
	}
	g.trackHover(e)
	switch e.Kind {
	case EventMouseDown:
		if !g.bounds.Contains(e.X, e.Y) {
			return false
		}
		s.Focus(g)
		for i := range g.Items {
			if g.itemRect(i, hitTheme).Contains(e.X, e.Y) {
				g.set(i)
				break
			}
		}
		return true
	case EventKey:
		if !g.focused {
			return false
		}
		switch e.Key {
		case KeyDown, KeyRight:
			g.set(g.Selected + 1)
			return true
		case KeyUp, KeyLeft:
			g.set(g.Selected - 1)
			return true
		}
	}
	return false
}

// hitTheme is the theme the hit test uses. It has to be the same measurements
// as drawing or a click lands in the wrong place, so the screen updates it at
// the same time as it sets its own theme.
//
// hitTheme - 히트 테스트가 쓰는 테마. 그릴 때와 같은 치수를 써야 클릭
// 위치가 어긋나지 않으므로, 화면이 테마를 정할 때 함께 갱신한다.
var hitTheme = DefaultTheme()

// SetHitTheme lets a screen use its own theme for the widgets' hit tests too.
// SetHitTheme - 화면이 자기 테마를 위젯 히트 테스트에도 쓰게 한다.
func SetHitTheme(th *Theme) { hitTheme = th }

func (g *RadioGroup) set(i int) {
	if i < 0 || i >= len(g.Items) || i == g.Selected {
		return
	}
	g.Selected = i
	if g.OnChange != nil {
		g.OnChange(i)
	}
}
