// screen.go is the container that gathers widgets into one screen.
//
// It fixes the order events arrive in and the order things are drawn in, and it
// looks after focus and popups. While a popup (an open dropdown) is up it sees
// events first and is drawn last, so nothing underneath covers it.
//
// screen.go - 위젯을 모아 한 화면으로 다루는 컨테이너.
//
// 이벤트를 받을 순서와 그릴 순서를 정하고, 포커스와 팝업을 관리한다.
// 팝업(펼친 드롭다운) 이 열려 있으면 그것이 이벤트를 먼저 받고 마지막에
// 그려진다 - 그래야 아래 위젯에 가려지지 않는다.
package fbui

// Panel is the bundle of widgets making up one screen.
// Panel - 한 화면을 이루는 위젯 묶음.
type Panel struct {
	widgets []Widget
	focus   int
	popup   Popup
	theme   *Theme
	// dirty says whether anything changed since the last draw. Moving pixels to
	// the framebuffer is expensive, so nothing is redrawn when nothing changed.
	//
	// dirty - 마지막으로 그린 뒤 상태가 바뀌었는지. 프레임버퍼로 옮기는
	// 비용이 커서, 바뀐 게 없으면 다시 그리지 않는다.
	dirty bool
	// ownTab says whether this panel handles Tab itself. On a screen where several
	// panels form one tab order, the outer code turns this off and drives it.
	//
	// ownTab - 탭 키를 이 패널이 직접 처리할지. 여러 패널이 하나의 탭
	// 순서를 이뤄야 하는 화면에서는 바깥이 끄고 직접 돌린다.
	ownTab bool
}

// NewPanel is an empty screen.
// NewPanel - 빈 화면.
func NewPanel(th *Theme) *Panel {
	if th == nil {
		th = DefaultTheme()
	}
	SetHitTheme(th)
	return &Panel{focus: -1, theme: th, dirty: true, ownTab: true}
}

// SetOwnTabOrder says whether this panel handles Tab itself. Turned off, Tab
// passes straight through so something outside can chain several panels.
//
// SetOwnTabOrder - 탭 키를 이 패널이 직접 처리할지 정한다. 끄면 탭이
// 그대로 통과하므로 바깥이 여러 패널을 엮어 돌릴 수 있다.
func (p *Panel) SetOwnTabOrder(v bool) { p.ownTab = v }

// Theme is the theme this screen uses.
// Theme - 이 화면이 쓰는 테마.
func (p *Panel) Theme() *Theme { return p.theme }

// Add appends a widget. The order they are added in is the tab order.
// Add - 위젯을 끝에 붙인다. 붙인 순서가 탭 순서다.
func (p *Panel) Add(w ...Widget) {
	p.widgets = append(p.widgets, w...)
	p.dirty = true
}

// Clear empties every widget, for rebuilding the screen when a step changes.
// Clear - 위젯을 전부 비운다. 단계를 넘어갈 때 화면을 새로 꾸미려고 쓴다.
func (p *Panel) Clear() {
	p.widgets = nil
	p.focus = -1
	p.popup = nil
	p.dirty = true
}

// Widgets are the widgets held, used when working the layout out.
// Widgets - 담긴 위젯. 레이아웃 계산에 쓴다.
func (p *Panel) Widgets() []Widget { return p.widgets }

// Dirty reports whether a redraw is needed.
// Dirty - 다시 그려야 하는지.
func (p *Panel) Dirty() bool { return p.dirty }

// MarkDirty forces a redraw after something outside changed the state.
// MarkDirty - 바깥에서 상태를 바꿨을 때 다시 그리게 한다.
func (p *Panel) MarkDirty() { p.dirty = true }

// OpenPopup is part of the Screen interface. Only one popup is ever open.
// OpenPopup - Screen 인터페이스. 열린 팝업은 하나뿐이다.
func (p *Panel) OpenPopup(pop Popup) {
	p.popup = pop
	p.dirty = true
}

// ClosePopup is part of the Screen interface.
// ClosePopup - Screen 인터페이스.
func (p *Panel) ClosePopup() {
	p.popup = nil
	p.dirty = true
}

// Focus is part of the Screen interface; it moves focus to this widget.
// Focus - Screen 인터페이스. 포커스를 이 위젯으로 옮긴다.
func (p *Panel) Focus(w Widget) {
	for i, x := range p.widgets {
		if x == w {
			p.setFocus(i)
			return
		}
	}
}

// setFocus moves focus by index; -1 leaves nothing focused.
// setFocus - 인덱스로 포커스를 옮긴다. -1 이면 아무도 포커스 없음.
func (p *Panel) setFocus(i int) {
	if p.focus == i {
		return
	}
	if p.focus >= 0 && p.focus < len(p.widgets) {
		p.widgets[p.focus].SetFocused(false)
	}
	p.focus = i
	if i >= 0 && i < len(p.widgets) {
		p.widgets[i].SetFocused(true)
	}
	p.dirty = true
}

// FocusFirst moves focus to the first widget that can take it. It is called on
// every step change so the keyboard alone can drive the screen straight away.
//
// FocusFirst - 포커스를 받을 수 있는 첫 위젯으로. 단계가 바뀔 때마다
// 호출해서 키보드만으로도 바로 조작할 수 있게 한다.
func (p *Panel) FocusFirst() {
	for i, w := range p.widgets {
		if w.CanFocus() {
			p.setFocus(i)
			return
		}
	}
	p.setFocus(-1)
}

// FocusNext is the next (or previous) focusable widget in tab order.
// FocusNext - 탭 순서로 다음(또는 이전) 포커스 가능한 위젯.
func (p *Panel) FocusNext(back bool) {
	n := len(p.widgets)
	if n == 0 {
		return
	}
	step := 1
	if back {
		step = -1
	}
	start := p.focus
	if start < 0 {
		start = -1
		if back {
			start = n
		}
	}
	for k := 1; k <= n; k++ {
		i := ((start+step*k)%n + n) % n
		if p.widgets[i].CanFocus() {
			p.setFocus(i)
			return
		}
	}
}

// Handle takes one event; true if anything consumed it.
// Handle - 이벤트 하나를 처리한다. 누군가 소비하면 true.
func (p *Panel) Handle(e Event) bool {
	// The popup first. A click outside the list is not consumed by the popup, only
	// closes it, so that click flows on to the widget underneath.
	//
	// 팝업이 먼저다. 목록 밖 클릭은 팝업이 소비하지 않고 닫기만 하므로,
	// 그 클릭은 아래 위젯으로 그대로 흘러간다.
	if p.popup != nil {
		p.dirty = true
		if p.popup.HandlePopup(e, p) {
			return true
		}
	}

	// Tab movement is handled here rather than by the individual widgets. A widget
	// intercepting it can trap the focus.
	//
	// 탭 이동은 개별 위젯이 아니라 여기서 처리한다. 위젯이 가로채면
	// 포커스가 갇히는 경우가 생긴다.
	if p.ownTab && e.Kind == EventKey {
		switch e.Key {
		case KeyTab:
			p.FocusNext(false)
			return true
		case KeyShiftTab:
			p.FocusNext(true)
			return true
		}
	}

	// The focused widget gets it first, so a keypress does not land on the wrong
	// widget.
	//
	// 포커스된 위젯에 먼저 준다. 키 입력이 엉뚱한 위젯으로 가지 않게.
	if p.focus >= 0 && p.focus < len(p.widgets) {
		if p.widgets[p.focus].Handle(e, p) {
			p.dirty = true
			return true
		}
	}
	for i, w := range p.widgets {
		if i == p.focus {
			continue
		}
		if w.Handle(e, p) {
			p.dirty = true
			return true
		}
	}
	// A mouse move can have changed a hover state even when nobody consumed it.
	// 마우스 이동은 아무도 소비하지 않아도 hover 상태를 바꿨을 수 있다.
	if e.Kind == EventMouseMove {
		p.dirty = true
	}
	return false
}

// Draw paints the widgets in order.
//
// The popup is not drawn here. The screen sometimes calls this with the clip
// already narrowed to the body area, and drawing inside that would cut an open
// list off at the boundary. The popup is drawn separately by DrawPopupOverlay
// once the clip is released.
//
// Draw - 위젯을 순서대로 그린다.
//
// 팝업은 여기서 그리지 않는다. 화면이 본문 영역으로 클립을 걸어둔 채
// 부르는 경우가 있어서, 그 안에서 그리면 펼친 목록이 영역 경계에서
// 잘린다. 팝업은 클립을 푼 뒤 DrawPopupOverlay 로 따로 그린다.
func (p *Panel) Draw(c *Canvas) {
	for _, w := range p.widgets {
		w.Draw(c, p.theme)
	}
	p.dirty = false
}

// DrawPopupOverlay draws the open popup on top. It has to be called with the
// clip released.
//
// DrawPopupOverlay - 열린 팝업을 맨 위에 그린다. 클립이 풀린 상태에서
// 불러야 한다.
func (p *Panel) DrawPopupOverlay(c *Canvas) {
	if p.popup != nil {
		p.popup.DrawPopup(c, p.theme)
	}
}

// AdvanceFocus moves one place along the tab order.
//
// Past the end it drops focus and returns false, so that where a screen is
// split across several panels (the body and the button bar) the caller can hand
// over to the next one. Unlike FocusNext, which cycles on its own, this does not
// wrap around.
//
// AdvanceFocus - 탭 순서로 한 칸 옮긴다.
//
// 끝을 넘어가면 포커스를 풀고 false 를 돌려준다. 화면이 여러 패널로
// 나뉘어 있을 때 (본문과 버튼 막대) 호출자가 다음 패널로 넘길 수 있게
// 하려는 것이다. 혼자 도는 FocusNext 와 달리 여기서는 되감지 않는다.
func (p *Panel) AdvanceFocus(back bool) bool {
	n := len(p.widgets)
	if n == 0 {
		return false
	}
	step := 1
	if back {
		step = -1
	}
	for i := p.focus + step; i >= 0 && i < n; i += step {
		if p.widgets[i].CanFocus() {
			p.setFocus(i)
			return true
		}
	}
	p.setFocus(-1)
	return false
}

// FocusEdge puts focus at one end: the last widget with back set, the first
// otherwise. It is the entry point when Tab arrives from another panel. With no
// focusable widget at all it returns false.
//
// FocusEdge - 포커스를 한쪽 끝에 놓는다. back 이면 마지막, 아니면 첫
// 위젯. 다른 패널에서 탭이 넘어올 때 진입 지점으로 쓴다. 포커스를 받을
// 위젯이 하나도 없으면 false.
func (p *Panel) FocusEdge(back bool) bool {
	if back {
		for i := len(p.widgets) - 1; i >= 0; i-- {
			if p.widgets[i].CanFocus() {
				p.setFocus(i)
				return true
			}
		}
	} else {
		for i := range p.widgets {
			if p.widgets[i].CanFocus() {
				p.setFocus(i)
				return true
			}
		}
	}
	p.setFocus(-1)
	return false
}

// HasFocus reports whether any widget in this panel is focused.
// HasFocus - 이 패널 안에 포커스된 위젯이 있는지.
func (p *Panel) HasFocus() bool { return p.focus >= 0 }

// DropFocus releases focus.
// DropFocus - 포커스를 푼다.
func (p *Panel) DropFocus() { p.setFocus(-1) }

// FocusAt focuses by index, doing nothing if the index is out of range or the
// widget cannot take focus. It restores the previous focus after the body has
// been rebuilt.
//
// FocusAt - 인덱스로 포커스를 지정한다. 범위 밖이거나 포커스 불가면
// 아무 것도 안 한다. 본문을 다시 지은 뒤 이전 포커스를 되돌릴 때 쓴다.
func (p *Panel) FocusAt(i int) {
	if i < 0 || i >= len(p.widgets) || !p.widgets[i].CanFocus() {
		return
	}
	p.setFocus(i)
}

// FocusedIndex is the index of the focused widget, or -1 when there is none.
// FocusedIndex - 지금 포커스된 위젯의 인덱스. 없으면 -1.
func (p *Panel) FocusedIndex() int { return p.focus }
