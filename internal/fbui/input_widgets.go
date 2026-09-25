// input_widgets.go holds the widgets for choosing or entering a value.
//
// An open dropdown covers the widgets below it, so it registers as a popup and
// is drawn last. Otherwise another widget would end up drawn over the top of it.
//
// input_widgets.go - 값을 고르거나 입력하는 위젯.
//
// 드롭다운은 목록이 열리면 아래쪽 위젯을 덮으므로 팝업으로 등록해 맨
// 마지막에 그린다. 그래야 다른 위젯이 그 위에 겹쳐 그려지지 않는다.
package fbui

import "strings"

// popupRowH is the row height of an open list. Drawing stores the value it got
// from the theme here and the hit test uses the same one. One theme draws the
// whole screen, so the two can never disagree.
//
// popupRowH - 펼친 목록의 행 높이. 그릴 때 테마에서 받은 값을 여기
// 저장해 두고 히트 테스트가 같은 값을 쓴다. 테마 하나로 화면 전체를
// 그리므로 값이 갈릴 일이 없다.
var popupRowH = 34

// Dropdown is the control for choosing one of a fixed set of items.
// Dropdown - 정해진 항목 중 하나를 고르는 컨트롤.
type Dropdown struct {
	base
	// Items are what is shown; the value is its own label.
	// Items - 보여줄 항목. 값 자체가 라벨이다.
	Items []string
	// Selected is the index chosen, or -1 when nothing is.
	// Selected - 고른 항목의 인덱스. 아무것도 안 골랐으면 -1.
	Selected int
	// OnChange is called when the choice changes, with the new index.
	// OnChange - 고른 항목이 바뀌면 호출. 새 인덱스를 받는다.
	OnChange func(int)
	// Placeholder is shown while nothing is chosen.
	// Placeholder - 아무것도 안 골랐을 때 보여줄 글자.
	Placeholder string
	// MaxVisible is how many rows an open list shows at once; past that it scrolls.
	// MaxVisible - 펼쳤을 때 한 번에 보여줄 줄 수. 넘치면 스크롤.
	MaxVisible int

	open bool
	// highlight is the row the mouse or the arrow keys point at in an open list.
	// highlight - 펼친 목록에서 마우스나 방향키가 가리키는 줄.
	highlight int
	// scroll is the index of the first row of an open list.
	// scroll - 펼친 목록의 첫 줄 인덱스.
	scroll int
	// popup is the last computed area of the open list.
	// popup - 마지막으로 계산한 펼친 목록 영역.
	popup Rect
}

// NewDropdown builds a dropdown from a list of items and an initial choice.
// NewDropdown - 항목 목록과 초기 선택으로 드롭다운을 만든다.
func NewDropdown(items []string, selected int, onChange func(int)) *Dropdown {
	if selected < 0 || selected >= len(items) {
		selected = -1
	}
	return &Dropdown{
		Items: items, Selected: selected, OnChange: onChange,
		MaxVisible: 8, highlight: selected,
	}
}

// Value is the chosen item as a string, or empty when there is none.
// Value - 지금 고른 항목의 문자열. 없으면 빈 문자열.
func (d *Dropdown) Value() string {
	if d.Selected < 0 || d.Selected >= len(d.Items) {
		return ""
	}
	return d.Items[d.Selected]
}

// Select chooses by index, clearing the choice when the index is out of range.
// It does not call OnChange - this is the path the program pushes a value in
// by, and running the callback from here is an easy way to loop forever.
//
// Select - 인덱스로 고른다. 범위 밖이면 선택을 비운다. OnChange 는
// 부르지 않는다 - 프로그램이 값을 밀어넣는 경로라 콜백이 다시 돌면
// 무한 루프가 되기 쉽다.
func (d *Dropdown) Select(i int) {
	if i < 0 || i >= len(d.Items) {
		d.Selected = -1
	} else {
		d.Selected = i
	}
	d.highlight = d.Selected
}

func (d *Dropdown) Draw(c *Canvas, th *Theme) {
	bg, fg := th.Control, th.Text
	switch {
	case d.disabled:
		bg, fg = th.ControlDim, th.TextDim
	case d.hovered || d.open:
		bg = th.ControlHover
	}
	c.FillRound(d.bounds, th.Radius, bg)
	border := th.Border
	if d.focused || d.open {
		border = th.BorderFocus
	}
	c.BorderRound(d.bounds, th.Radius, 1, border)

	// Leave room for the arrow at the right edge and put the text in the rest.
	// 오른쪽 끝에 화살표 자리를 비우고 나머지에 글자를 넣는다.
	arrowW := d.bounds.H
	textArea := Rect{d.bounds.X + 8, d.bounds.Y, d.bounds.W - arrowW - 8, d.bounds.H}
	label, col := d.Value(), fg
	if label == "" {
		label, col = d.Placeholder, th.TextDim
	}
	c.TextIn(textArea, th.FontBody.Truncate(label, textArea.W), col, th.FontBody, -1)

	// A triangle pointing down, filled by narrowing the horizontal lines.
	// 아래를 가리키는 삼각형. 가로줄을 좁혀가며 채운다.
	ax := d.bounds.X + d.bounds.W - arrowW/2 - 1
	ay := d.bounds.Y + d.bounds.H/2 - 2
	for i := 0; i < 5; i++ {
		c.Fill(Rect{ax - 4 + i, ay + i, 9 - 2*i, 1}, fg)
	}
}

// popupRect is the area an open list will take. If it would run off the bottom
// of the screen it opens upwards instead.
//
// popupRect - 펼친 목록이 차지할 영역. 화면 아래로 넘치면 위로 편다.
func (d *Dropdown) popupRect(screenH, rowH int) Rect {
	n := len(d.Items)
	if n > d.MaxVisible {
		n = d.MaxVisible
	}
	h := n*rowH + 2
	below := d.bounds.Y + d.bounds.H
	if below+h <= screenH {
		return Rect{d.bounds.X, below, d.bounds.W, h}
	}
	if above := d.bounds.Y - h; above >= 0 {
		return Rect{d.bounds.X, above, d.bounds.W, h}
	}
	// With no room either way it opens downwards and is cut off at the
	// bottom of the screen.
	//
	// 위아래 어디에도 안 들어가면 아래로 펴고 화면 끝에서 자른다.
	return Rect{d.bounds.X, below, d.bounds.W, screenH - below}
}

// PopupBounds is the area the open list occupies.
// PopupBounds - 펼친 목록이 차지한 영역.
func (d *Dropdown) PopupBounds() Rect { return d.popup }

// DrawPopup paints the open list over everything else.
// DrawPopup - 펼친 목록을 다른 것들 위에 그린다.
func (d *Dropdown) DrawPopup(c *Canvas, th *Theme) {
	if !d.open {
		return
	}
	rowH := th.RowH
	popupRowH = rowH
	r := d.popupRect(c.H, rowH)
	d.popup = r

	c.Shadow(r, th.Radius, 70)
	c.FillRound(r, th.Radius, th.Control)
	c.BorderRound(r, th.Radius, 1, th.BorderFocus)

	inner := r.Inset(1)
	old := c.Clip(inner)
	visible := inner.H / rowH
	for i := 0; i < visible; i++ {
		idx := d.scroll + i
		if idx >= len(d.Items) {
			break
		}
		row := Rect{inner.X, inner.Y + i*rowH, inner.W, rowH}
		switch {
		case idx == d.highlight:
			c.Fill(row, th.Selection)
		case idx == d.Selected:
			c.Fill(row, Blend(th.Control, th.Selection, 90))
		}
		text := Rect{row.X + 8, row.Y, row.W - 16, row.H}
		c.TextIn(text, th.FontBody.Truncate(d.Items[idx], text.W), th.Text, th.FontBody, -1)
	}
	c.SetClip(old)

	// A position bar on the right, where the list can scroll.
	// 스크롤 가능하면 오른쪽에 위치 막대를 그린다.
	if visible > 0 && len(d.Items) > visible {
		thumbH := inner.H * visible / len(d.Items)
		if thumbH < 8 {
			thumbH = 8
		}
		off := 0
		if maxScroll := len(d.Items) - visible; maxScroll > 0 {
			off = (inner.H - thumbH) * d.scroll / maxScroll
		}
		c.Fill(Rect{inner.X + inner.W - 5, inner.Y + off, 4, thumbH}, th.Border)
	}
}

func (d *Dropdown) HandlePopup(e Event, s Screen) bool {
	if !d.open {
		return false
	}
	switch e.Kind {
	case EventMouseMove:
		if idx, ok := d.rowAt(e.X, e.Y); ok {
			d.highlight = idx
		}
		return true
	case EventMouseDown:
		if !d.popup.Contains(e.X, e.Y) && !d.bounds.Contains(e.X, e.Y) {
			// A click outside the list closes it without choosing. That click has to
			// reach the widget underneath too, so it is not consumed.
			//
			// 목록 밖을 누르면 고르지 않고 닫는다. 그 클릭은 아래
			// 위젯도 받아야 하므로 소비하지 않는다.
			d.closeWith(s, false)
			return false
		}
		return true
	case EventMouseUp:
		if idx, ok := d.rowAt(e.X, e.Y); ok {
			d.highlight = idx
			d.closeWith(s, true)
		}
		return true
	case EventWheel:
		d.scrollBy(-e.Delta)
		return true
	case EventKey:
		switch e.Key {
		case KeyDown:
			d.moveHighlight(1)
		case KeyUp:
			d.moveHighlight(-1)
		case KeyPageDown:
			d.moveHighlight(d.MaxVisible)
		case KeyPageUp:
			d.moveHighlight(-d.MaxVisible)
		case KeyHome:
			d.highlight = 0
			d.ensureVisible()
		case KeyEnd:
			d.highlight = len(d.Items) - 1
			d.ensureVisible()
		case KeyEnter, KeySpace:
			d.closeWith(s, true)
		case KeyEscape, KeyTab:
			d.closeWith(s, false)
		default:
			// Pressing a letter jumps to the item starting with it.
			// 글자를 누르면 그 글자로 시작하는 항목으로 뛴다.
			if e.Rune != 0 {
				d.jumpTo(e.Rune)
			}
		}
		return true
	}
	return true
}

// rowAt is the item index a screen coordinate points at.
// rowAt - 화면 좌표가 가리키는 항목 인덱스.
func (d *Dropdown) rowAt(x, y int) (int, bool) {
	inner := d.popup.Inset(1)
	if !inner.Contains(x, y) {
		return 0, false
	}
	idx := d.scroll + (y-inner.Y)/popupRowH
	if idx < 0 || idx >= len(d.Items) {
		return 0, false
	}
	return idx, true
}

func (d *Dropdown) moveHighlight(delta int) {
	if len(d.Items) == 0 {
		return
	}
	d.highlight += delta
	if d.highlight < 0 {
		d.highlight = 0
	}
	if d.highlight >= len(d.Items) {
		d.highlight = len(d.Items) - 1
	}
	d.ensureVisible()
}

func (d *Dropdown) scrollBy(delta int) {
	maxScroll := len(d.Items) - d.visibleCount()
	if maxScroll < 0 {
		maxScroll = 0
	}
	d.scroll += delta
	if d.scroll < 0 {
		d.scroll = 0
	}
	if d.scroll > maxScroll {
		d.scroll = maxScroll
	}
}

func (d *Dropdown) visibleCount() int {
	n := d.MaxVisible
	if n > len(d.Items) {
		n = len(d.Items)
	}
	if n < 1 {
		n = 1
	}
	return n
}

// ensureVisible scrolls so that highlight is on screen.
// ensureVisible - highlight 가 보이도록 스크롤을 맞춘다.
func (d *Dropdown) ensureVisible() {
	visible := d.visibleCount()
	if d.highlight < d.scroll {
		d.scroll = d.highlight
	}
	if d.highlight >= d.scroll+visible {
		d.scroll = d.highlight - visible + 1
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
}

func (d *Dropdown) jumpTo(r rune) {
	want := strings.ToLower(string(r))
	for i, it := range d.Items {
		if strings.HasPrefix(strings.ToLower(it), want) {
			d.highlight = i
			d.ensureVisible()
			return
		}
	}
}

// closeWith closes the list; with commit set, highlight becomes the choice.
// closeWith - 목록을 닫는다. commit 이면 highlight 를 선택으로 확정한다.
func (d *Dropdown) closeWith(s Screen, commit bool) {
	d.open = false
	s.ClosePopup()
	if !commit {
		return
	}
	if d.highlight < 0 || d.highlight >= len(d.Items) || d.highlight == d.Selected {
		return
	}
	d.Selected = d.highlight
	if d.OnChange != nil {
		d.OnChange(d.Selected)
	}
}

func (d *Dropdown) Handle(e Event, s Screen) bool {
	if d.disabled {
		return false
	}
	d.trackHover(e)
	switch e.Kind {
	case EventMouseDown:
		if d.bounds.Contains(e.X, e.Y) {
			s.Focus(d)
			d.toggle(s)
			return true
		}
	case EventKey:
		if !d.focused {
			return false
		}
		switch e.Key {
		case KeySpace:
			// Enter is deliberately not taken. In the wizard, Enter means "next step",
			// and a closed dropdown intercepting it would make the wizard impossible to
			// advance from the keyboard alone. Opening the list is Space's job; the
			// arrow keys step through the items without opening it.
			//
			// 엔터는 일부러 받지 않는다. 마법사에서 엔터는 "다음 단계" 라서,
			// 닫힌 드롭다운이 그것을 가로채면 키보드만으로는 진행을 못 한다.
			// 목록을 여는 것은 스페이스가 맡고, 방향키는 목록을 열지 않고 항목을
			// 넘긴다.
			d.toggle(s)
			return true
		case KeyDown:
			// So the next item can be reached without opening the list.
			// 목록을 열지 않고도 옆 항목으로 넘어갈 수 있게 한다.
			d.step(1)
			return true
		case KeyUp:
			d.step(-1)
			return true
		}
	}
	return false
}

func (d *Dropdown) toggle(s Screen) {
	if len(d.Items) == 0 {
		return
	}
	if d.open {
		d.closeWith(s, false)
		return
	}
	d.open = true
	d.highlight = d.Selected
	if d.highlight < 0 {
		d.highlight = 0
	}
	d.ensureVisible()
	s.OpenPopup(d)
}

func (d *Dropdown) step(delta int) {
	if len(d.Items) == 0 {
		return
	}
	i := d.Selected + delta
	if i < 0 {
		i = 0
	}
	if i >= len(d.Items) {
		i = len(d.Items) - 1
	}
	if i == d.Selected {
		return
	}
	d.Selected = i
	d.highlight = i
	if d.OnChange != nil {
		d.OnChange(i)
	}
}
