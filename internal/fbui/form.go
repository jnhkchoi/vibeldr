// form.go is the layout helper that stacks controls down the body area.
//
// Every step repeats "one label row, one control row", so rather than working
// coordinates out by hand each time, it carries a single cursor downwards. Once
// the remaining height runs out it stops stacking, quietly - a widget placed off
// screen cannot be clicked, and that is a hard cause to track down.
//
// form.go - 본문 영역에 컨트롤을 위에서 아래로 쌓는 배치 도우미.
//
// 단계마다 "라벨 한 줄 + 컨트롤 한 줄" 이 반복되므로, 좌표를 매번 손으로
// 계산하지 않도록 커서 하나를 들고 내려간다. 남은 높이를 넘기면 더 쌓지
// 않고 조용히 멈춘다 - 화면 밖에 위젯이 놓이면 클릭이 안 되는데 원인을
// 찾기 어렵다.
package fbui

// Form is the cursor that stacks things vertically.
// Form - 세로로 쌓는 배치 커서.
type Form struct {
	area Rect
	y    int
	// LabelW is the width of the label column; the control starts right after
	// it on the same row. 0 is not special: the label gets no room and the
	// control takes the whole row.
	//
	// LabelW - 라벨 열의 폭. 컨트롤은 같은 줄의 그 뒤에서 시작한다. 0 이라고
	// 따로 다루지 않는다: 라벨은 자리를 못 얻고 컨트롤이 줄 전체를 차지한다.
	LabelW int
	// Gap is the space between rows.
	// Gap - 줄 사이 간격.
	Gap int
	th  *Theme
	out []Widget
}

// NewForm starts stacking inside an area.
// NewForm - 영역 안에 쌓기 시작한다.
func NewForm(area Rect, th *Theme) *Form {
	return &Form{area: area, y: area.Y, LabelW: 180, Gap: 10, th: th}
}

// Widgets are the widgets placed so far, handed straight to a Panel.
// Widgets - 지금까지 놓인 위젯. Panel 에 그대로 넘긴다.
func (f *Form) Widgets() []Widget { return f.out }

// Remaining is the vertical space still available.
// Remaining - 아직 쓸 수 있는 세로 공간.
func (f *Form) Remaining() int { return f.area.Y + f.area.H - f.y }

// Cursor is the y the next row will be placed at.
// Cursor - 다음 줄이 놓일 y 좌표.
func (f *Form) Cursor() int { return f.y }

// fits reports whether another row of height h still goes in.
// fits - 높이 h 짜리 줄을 더 놓을 수 있는지.
func (f *Form) fits(h int) bool { return f.Remaining() >= h }

// Space leaves a blank row.
// Space - 빈 줄을 넣는다.
func (f *Form) Space(h int) {
	if f.fits(h) {
		f.y += h
	}
}

// Row is one row of label on the left and control on the right. An h of 0 means
// the theme's RowH.
//
// Row - 왼쪽 라벨과 오른쪽 컨트롤 한 줄. h 가 0 이면 테마의 RowH.
func (f *Form) Row(label string, w Widget, h int) Widget {
	if h == 0 {
		h = f.th.RowH
	}
	if !f.fits(h) {
		return w
	}
	if label != "" {
		l := NewLabel(label)
		l.SetBounds(Rect{f.area.X, f.y, f.LabelW - 10, h})
		f.out = append(f.out, l)
	}
	if w != nil {
		w.SetBounds(Rect{f.area.X + f.LabelW, f.y, f.area.W - f.LabelW, h})
		f.out = append(f.out, w)
	}
	f.y += h + f.Gap
	return w
}

// RowWidth is Row with the control's width given. A dropdown spanning the whole
// row reads worse, not better, so this narrows it to the length of its value.
//
// RowWidth - Row 와 같지만 컨트롤 폭을 지정한다. 드롭다운이 줄 전체를
// 차지하면 오히려 읽기 나빠서, 값 길이에 맞춰 줄일 때 쓴다.
func (f *Form) RowWidth(label string, w Widget, width, h int) Widget {
	if h == 0 {
		h = f.th.RowH
	}
	if !f.fits(h) {
		return w
	}
	if label != "" {
		l := NewLabel(label)
		l.SetBounds(Rect{f.area.X, f.y, f.LabelW - 10, h})
		f.out = append(f.out, l)
	}
	if w != nil {
		if width > f.area.W-f.LabelW {
			width = f.area.W - f.LabelW
		}
		w.SetBounds(Rect{f.area.X + f.LabelW, f.y, width, h})
		f.out = append(f.out, w)
	}
	f.y += h + f.Gap
	return w
}

// Full is one widget taking the full width of a row.
// Full - 줄 전체 폭을 쓰는 위젯 하나.
func (f *Form) Full(w Widget, h int) Widget {
	if h == 0 {
		h = f.th.RowH
	}
	if !f.fits(h) || w == nil {
		return w
	}
	w.SetBounds(Rect{f.area.X, f.y, f.area.W, h})
	f.out = append(f.out, w)
	f.y += h + f.Gap
	return w
}

// Text is one line of explanation - guidance rather than a control.
// Text - 설명 문단 한 줄. 컨트롤이 아니라 안내 글일 때.
func (f *Form) Text(s string, dim bool) *Label {
	h := f.th.FontBody.Height() + 4
	if dim {
		h = f.th.FontSmall.Height() + 4
	}
	if !f.fits(h) {
		return nil
	}
	l := NewLabel(s)
	l.Dim = dim
	if dim {
		l.Font = f.th.FontSmall
	}
	l.SetBounds(Rect{f.area.X, f.y, f.area.W, h})
	f.out = append(f.out, l)
	f.y += h
	return l
}

// Columns splits one row into several cells and puts widgets side by side.
// weights are the relative widths and must be as many as the widgets. It is for
// laying one disk out as "name - capacity - bay dropdown".
//
// Columns - 한 줄을 여러 칸으로 나눠 위젯을 나란히 놓는다. weights 는 각
// 칸의 상대 폭이고, 위젯 수와 길이가 같아야 한다. 디스크 한 대를 "이름 -
// 용량 - 베이 드롭다운" 처럼 늘어놓을 때 쓴다.
func (f *Form) Columns(h int, weights []int, ws []Widget) {
	if h == 0 {
		h = f.th.RowH
	}
	if !f.fits(h) || len(weights) != len(ws) || len(ws) == 0 {
		return
	}
	total := 0
	for _, v := range weights {
		total += v
	}
	if total <= 0 {
		return
	}
	const colGap = 8
	avail := f.area.W - colGap*(len(ws)-1)
	x := f.area.X
	for i, w := range ws {
		cw := avail * weights[i] / total
		if w != nil {
			w.SetBounds(Rect{x, f.y, cw, h})
			f.out = append(f.out, w)
		}
		x += cw + colGap
	}
	f.y += h + f.Gap
}

// Separator is one horizontal rule, for parting a list's heading from its body.
// Separator - 가로 구분선 한 줄. 목록의 머리글과 본문을 가를 때.
func (f *Form) Separator() {
	const h = 9
	if !f.fits(h) {
		return
	}
	s := &separator{}
	s.SetBounds(Rect{f.area.X, f.y + h/2, f.area.W, 1})
	f.out = append(f.out, s)
	f.y += h
}

// separator is the display-only widget that draws nothing but a line.
// separator - 선 하나만 그리는 표시용 위젯.
type separator struct{ base }

// CanFocus is false; a rule takes no input.
// CanFocus - 구분선은 입력을 받지 않는다.
func (s *separator) CanFocus() bool { return false }

// Draw paints the one-pixel rule.
// Draw - 1 픽셀짜리 선을 그린다.
func (s *separator) Draw(c *Canvas, th *Theme) {
	c.Fill(Rect{s.bounds.X, s.bounds.Y, s.bounds.W, 1}, th.Border)
}

// Handle ignores every event.
// Handle - 이벤트를 받지 않는다.
func (s *separator) Handle(Event, Screen) bool { return false }
