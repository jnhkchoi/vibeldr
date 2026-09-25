// wizard.go is the frame of the install screen that walks through the steps.
//
// The screen has four parts: the title bar across the top, the step list on the
// left, the body in the middle and the button bar along the bottom. Only the
// body changes from step to step; the rest stays as the frame.
//
// wizard.go - 단계를 차례로 밟는 설치 화면의 틀.
//
// 화면은 네 부분으로 나뉜다: 위쪽 제목 막대, 왼쪽 단계 목록, 가운데 본문,
// 아래쪽 버튼 막대. 단계마다 바뀌는 것은 본문뿐이고 나머지는 틀로 남는다.
//
//	┌──────────────────────────────────────────┐
//	│ 제목 막대                                 │
//	├────────────┬─────────────────────────────┤
//	│ 1 시작     │ 단계 제목                    │
//	│ 2 모델     │ 설명                         │
//	│ ...        │ (본문 위젯)                  │
//	├────────────┴─────────────────────────────┤
//	│                    [뒤로] [다음] [취소]   │
//	└──────────────────────────────────────────┘
package fbui

// Step is one step of the wizard.
// Step - 마법사 한 단계.
type Step struct {
	// Title is the short name for the step list on the left.
	// Title - 왼쪽 단계 목록에 쓸 짧은 이름.
	Title string
	// Heading is the heading at the top of the body; empty uses Title.
	// Heading - 본문 맨 위 제목. 비우면 Title 을 쓴다.
	Heading string
	// Subtitle is the one-line explanation under the heading.
	// Subtitle - 제목 아래 한 줄 설명.
	Subtitle string

	// Build lays the body widgets out. body is the area available, and this is
	// called afresh on every entry into the step. Anything that can differ each
	// time, such as the hardware list, is read here.
	//
	// Build - 본문 위젯을 구성한다. body 는 쓸 수 있는 영역이고, 단계에
	// 들어올 때마다 새로 불린다. 하드웨어 목록처럼 매번 달라질 수 있는
	// 내용을 다시 읽으려면 여기서 한다.
	Build func(w *Wizard, p *Panel, body Rect)

	// Validate says whether moving on is allowed. An empty string passes;
	// anything else is shown in red on the button bar and blocks the move.
	//
	// Validate - 다음으로 넘어가도 되는지. 빈 문자열이면 통과, 아니면 그
	// 메시지를 버튼 막대에 빨간 글씨로 보여주고 막는다.
	Validate func() string

	// NextLabel is what the Next button says on this step; empty means "다음 >",
	// or "완료" on the last step.
	// NextLabel - 이 단계에서 다음 버튼에 쓸 글자. 비우면 "다음 >", 마지막
	// 단계면 "완료".
	NextLabel string

	// HideBack hides the Back button, on the first step and during an install.
	// HideBack - 뒤로 버튼을 감출지. 첫 단계와 설치 진행 중에 쓴다.
	HideBack bool
	// HideNext hides the Next button, for a step the user cannot move on from,
	// such as a progress screen.
	//
	// HideNext - 다음 버튼을 감출지. 진행 중 화면처럼 사용자가 넘길 수
	// 없는 단계에 쓴다.
	HideNext bool
}

// Wizard holds the steps and draws the screen.
// Wizard - 단계들을 들고 화면을 그리는 주체.
type Wizard struct {
	// Title is the name in the title bar.
	// Title - 제목 막대에 쓸 이름.
	Title string
	// Subtitle is the small extra written to the right of the title, such as a
	// version.
	//
	// Subtitle - 제목 오른쪽에 작게 쓸 부가 정보 (버전 등).
	Subtitle string

	steps []Step
	cur   int

	theme  *Theme
	canvas *Canvas
	body   *Panel
	// chrome holds the frame's own widgets: the buttons on the button bar.
	// chrome - 틀에 속한 위젯 (버튼 막대의 버튼들).
	chrome *Panel

	backBtn, nextBtn, cancelBtn *Button

	// status is the message shown at the left of the button bar.
	// status - 버튼 막대 왼쪽에 보여줄 메시지.
	status string
	// statusErr draws status in the error colour.
	// statusErr - status 를 오류색으로 그릴지.
	statusErr bool

	// OnCancel is the Cancel button; nil hides the button.
	// OnCancel - 취소 버튼. nil 이면 취소 버튼을 감춘다.
	OnCancel func()
	// OnFinish runs when Next is pressed on the last step.
	// OnFinish - 마지막 단계에서 다음을 눌렀을 때.
	OnFinish func()

	// done says whether the wizard has finished; the loop watches it to leave.
	// done - 마법사가 끝났는지. 루프가 이걸 보고 빠져나간다.
	done bool

	// pick is a helper the tests use to check that RebuildBody rebuilds the
	// body with a new choice. Production code never touches it.
	//
	// pick - 테스트에서 RebuildBody 가 본문을 새 선택으로 다시 짓는지
	// 확인할 때 쓰는 보조 값. 프로덕션 코드는 쓰지 않는다.
	pick int
}

// NewWizard builds a wizard sized to a canvas.
// NewWizard - 캔버스 크기에 맞춰 마법사를 만든다.
func NewWizard(c *Canvas, th *Theme, title string, steps []Step) *Wizard {
	if th == nil {
		th = DefaultTheme()
	}
	SetHitTheme(th)
	w := &Wizard{
		Title:  title,
		steps:  steps,
		theme:  th,
		canvas: c,
		body:   NewPanel(th),
		chrome: NewPanel(th),
	}
	// The wizard chains the body and the button bar into one tab order.
	// 탭 순서는 마법사가 본문과 버튼 막대를 이어 돌린다.
	w.body.SetOwnTabOrder(false)
	w.chrome.SetOwnTabOrder(false)
	w.buildChrome()
	w.enter(0)
	return w
}

// Theme is the theme in use.
// Theme - 쓰고 있는 테마.
func (w *Wizard) Theme() *Theme { return w.theme }

// Canvas is what is drawn on.
// Canvas - 그리는 대상.
func (w *Wizard) Canvas() *Canvas { return w.canvas }

// Done reports whether the wizard has finished.
// Done - 마법사가 끝났는지.
func (w *Wizard) Done() bool { return w.done }

// Finish ends the wizard, called when the work on a progress step is done.
// Finish - 마법사를 끝낸다. 진행 단계에서 작업이 끝났을 때 부른다.
func (w *Wizard) Finish() { w.done = true }

// Step is the current step number, counting from 0.
// Step - 현재 단계 번호 (0 부터).
func (w *Wizard) Step() int { return w.cur }

// SetStatus sets the message on the button bar; err draws it in red.
// SetStatus - 버튼 막대에 보여줄 메시지. err 면 빨간색.
func (w *Wizard) SetStatus(msg string, err bool) {
	w.status, w.statusErr = msg, err
	w.chrome.MarkDirty()
}

// SetNextEnabled enables or disables the Next button, to lock it while work runs.
// SetNextEnabled - 다음 버튼을 쓸 수 있는지. 진행 중에 잠그는 용도.
func (w *Wizard) SetNextEnabled(v bool) {
	w.nextBtn.SetDisabled(!v)
	w.chrome.MarkDirty()
}

// Body is the current step's body screen. A step that has changed a widget's
// state calls MarkDirty on this to get a redraw.
//
// Body - 지금 단계의 본문 화면. 단계가 위젯 상태를 바꾼 뒤 다시 그리게
// 하려면 여기에 대고 MarkDirty 를 부른다.
func (w *Wizard) Body() *Panel { return w.body }

// Dirty reports whether a redraw is needed.
// Dirty - 다시 그려야 하는지.
func (w *Wizard) Dirty() bool { return w.body.Dirty() || w.chrome.Dirty() }

// MarkDirty forces the whole screen to be redrawn on the next frame.
// MarkDirty - 다음 프레임에 전체를 다시 그리게 한다.
func (w *Wizard) MarkDirty() {
	w.body.MarkDirty()
	w.chrome.MarkDirty()
}

// bodyRect is the area the body widgets have to work with.
// bodyRect - 본문 위젯이 쓸 수 있는 영역.
func (w *Wizard) bodyRect() Rect {
	th := w.theme
	// Take the height the heading and the subtitle occupy out first.
	// 제목과 설명이 차지하는 높이를 미리 뺀다.
	headH := th.FontHead.Height() + th.FontSmall.Height() + th.Pad*2
	return Rect{
		X: th.SidebarW + th.Pad*2,
		Y: th.TitleH + th.Pad + headH,
		W: w.canvas.W - th.SidebarW - th.Pad*3,
		H: w.canvas.H - th.TitleH - th.ButtonBarH - th.Pad*2 - headH,
	}
}

// buildChrome makes the button bar's buttons, which are kept across step changes.
// buildChrome - 버튼 막대의 버튼을 만든다. 단계가 바뀌어도 그대로 쓴다.
func (w *Wizard) buildChrome() {
	th := w.theme
	bh := 38
	bw := 120
	y := w.canvas.H - th.ButtonBarH + (th.ButtonBarH-bh)/2
	right := w.canvas.W - th.Pad

	w.cancelBtn = NewButton("취소", func() {
		if w.OnCancel != nil {
			w.OnCancel()
		}
	})
	w.cancelBtn.SetBounds(Rect{right - bw, y, bw, bh})

	w.nextBtn = NewButton("다음 >", w.Next)
	w.nextBtn.Primary = true
	w.nextBtn.SetBounds(Rect{right - bw*2 - 10, y, bw, bh})

	w.backBtn = NewButton("< 뒤로", w.Back)
	w.backBtn.SetBounds(Rect{right - bw*3 - 20, y, bw, bh})

	w.chrome.Add(w.backBtn, w.nextBtn, w.cancelBtn)
}

// enter goes into step i, building its body afresh.
// enter - i 번째 단계로 들어간다. 본문을 새로 짓는다.
func (w *Wizard) enter(i int) {
	if i < 0 || i >= len(w.steps) {
		return
	}
	w.cur = i
	w.status, w.statusErr = "", false

	st := w.steps[i]
	label := st.NextLabel
	if label == "" {
		label = "다음 >"
	}
	if i == len(w.steps)-1 && st.NextLabel == "" {
		label = "완료"
	}
	w.nextBtn.Text = label
	// A hidden button is disabled as well. Merely not drawing it would leave it
	// working when its place is clicked. This comes before Build, so a step can
	// lock the button while it works (the install screen does).
	//
	// 감춘 버튼은 비활성으로도 둔다. 안 그리기만 하면 그 자리를 눌렀을 때
	// 여전히 동작해 버린다. Build 보다 먼저 해서, 단계가 일하는 동안 버튼을
	// 잠글 수 있게 한다 (설치 화면이 그렇게 한다).
	w.nextBtn.SetDisabled(st.HideNext)
	w.nextBtn.base.hovered = false
	w.backBtn.SetDisabled(st.HideBack || i == 0)

	w.body.Clear()
	if st.Build != nil {
		st.Build(w, w.body, w.bodyRect())
	}
	w.chrome.DropFocus()
	w.body.FocusFirst()
	w.MarkDirty()
}

// RebuildBody rebuilds the current step's body widgets.
//
// It is for when a control such as a dropdown changed its value and another
// widget on the same screen - a card holding the chosen model's details, say -
// has to be redrawn from the new value. This is not a step change, so the status
// message and the buttons are left alone and focus stays on the widget that was
// just being used.
//
// RebuildBody - 현재 단계의 본문 위젯을 다시 짓는다.
//
// 드롭다운 같은 컨트롤이 값을 바꿔서, 같은 화면 안의 다른 위젯(예: 고른
// 모델 정보를 담은 카드) 을 새 값으로 다시 그려야 할 때 쓴다. 단계
// 이동이 아니므로 상태 메시지나 버튼은 건드리지 않고, 포커스는 방금
// 조작하던 위젯에 그대로 둔다.
func (w *Wizard) RebuildBody() {
	prevFocus := w.body.FocusedIndex()
	st := w.steps[w.cur]
	w.body.Clear()
	if st.Build != nil {
		st.Build(w, w.body, w.bodyRect())
	}
	// Rebuilding the body makes new widgets, but in the same order, so restoring
	// focus by the previous index puts it back on the control the user was using.
	//
	// 본문을 다시 지으면 위젯이 새로 생기지만 순서는 같으므로, 이전
	// 인덱스로 포커스를 되돌리면 사용자가 만지던 그 컨트롤로 돌아간다.
	if prevFocus >= 0 {
		w.body.FocusAt(prevFocus)
	} else {
		w.body.FocusFirst()
	}
	w.MarkDirty()
}

// Next moves on once Validate passes; on the last step it calls OnFinish.
// Next - 검사를 통과하면 다음 단계로. 마지막 단계면 OnFinish.
func (w *Wizard) Next() {
	st := w.steps[w.cur]
	if st.Validate != nil {
		if msg := st.Validate(); msg != "" {
			w.SetStatus(msg, true)
			return
		}
	}
	if w.cur == len(w.steps)-1 {
		if w.OnFinish != nil {
			w.OnFinish()
		}
		return
	}
	w.enter(w.cur + 1)
}

// Back goes to the previous step.
// Back - 이전 단계로.
func (w *Wizard) Back() {
	if w.cur > 0 {
		w.enter(w.cur - 1)
	}
}

// GoTo jumps to a given step. It runs no validation, so it is only for going
// back to a step that already passed, or forward to a progress screen.
//
// GoTo - 특정 단계로 건너뛴다. 검사를 하지 않으므로, 이미 통과한 단계로
// 되돌아가거나 진행 화면으로 넘길 때만 쓴다.
func (w *Wizard) GoTo(i int) { w.enter(i) }

// Handle takes one input: the body first, then the button bar.
// Handle - 입력 하나. 본문이 먼저, 그 다음 버튼 막대.
func (w *Wizard) Handle(e Event) {
	// Tab is handled by the screen as a whole. The body and the button bar are
	// different panels, and letting each cycle on its own would leave the buttons
	// unreachable by Tab. Where there is no mouse, that stops the wizard entirely.
	//
	// 탭은 화면 전체가 처리한다. 본문과 버튼 막대는 서로 다른 패널이라
	// 각자 돌게 두면 탭만으로는 버튼에 닿을 수 없다. 마우스가 없는
	// 환경에서 그러면 진행 자체가 막힌다.
	if e.Kind == EventKey && (e.Key == KeyTab || e.Key == KeyShiftTab) {
		// An open dropdown takes Tab for itself first and closes its list.
		// 펼친 드롭다운은 탭을 자기가 먼저 받아 목록을 닫는다.
		if w.body.Handle(e) {
			return
		}
		w.cycleFocus(e.Key == KeyShiftTab)
		return
	}

	// Enter moves to the next step only where the body does not use that key. A
	// text field or an open dropdown takes Enter first.
	//
	// 엔터로 다음 단계로 넘어가는 건 본문이 그 키를 안 쓸 때만이다. 입력칸이나
	// 펼친 드롭다운이 엔터를 먼저 가져간다.
	if w.body.Handle(e) {
		return
	}
	if w.chrome.Handle(e) {
		return
	}
	if e.Kind == EventKey {
		switch e.Key {
		case KeyEnter:
			if !w.nextBtn.Disabled() && !w.steps[w.cur].HideNext {
				w.Next()
			}
		case KeyEscape:
			if w.OnCancel != nil {
				w.OnCancel()
			}
		}
	}
}

// cycleFocus chains the body and the button bar into one tab order.
// cycleFocus - 본문과 버튼 막대를 하나의 탭 순서로 이어 돌린다.
func (w *Wizard) cycleFocus(back bool) {
	if w.chrome.HasFocus() {
		if w.chrome.AdvanceFocus(back) {
			return
		}
		// Having reached the end of the button bar, cross over to the far end of the
		// body.
		//
		// 버튼 막대 끝에 닿았으면 본문의 반대쪽 끝으로 넘어간다.
		if w.body.FocusEdge(back) {
			return
		}
		w.chrome.FocusEdge(back)
		return
	}
	if w.body.AdvanceFocus(back) {
		return
	}
	if w.chrome.FocusEdge(back) {
		return
	}
	w.body.FocusEdge(back)
}

// Draw paints the whole screen.
// Draw - 화면 전체.
func (w *Wizard) Draw() {
	th := w.theme
	c := w.canvas

	c.Fill(Rect{0, 0, c.W, c.H}, th.Panel)
	w.drawTitleBar()
	w.drawSidebar()
	w.drawHeading()

	old := c.Clip(w.bodyRect())
	w.body.Draw(c)
	c.SetClip(old)

	w.drawButtonBar()

	// An open dropdown can reach outside the body area, so it is drawn here,
	// after the clip is released. Panel.Draw does not draw popups at all.
	//
	// 펼친 드롭다운은 본문 영역을 벗어날 수 있으므로 클립을 푼 뒤 여기서
	// 그린다. Panel.Draw 는 팝업을 아예 그리지 않는다.
	w.body.DrawPopupOverlay(c)
}

func (w *Wizard) drawTitleBar() {
	th := w.theme
	c := w.canvas
	bar := Rect{0, 0, c.W, th.TitleH}
	c.Fill(bar, th.TitleBar)

	// A small mark in front of the name; text alone looks bare.
	// 이름 앞의 작은 표식. 글자만 있으면 허전하다.
	m := th.TitleH / 2
	c.FillRound(Rect{th.Pad, m - 9, 18, 18}, 4, th.Accent)
	c.FillRound(Rect{th.Pad + 5, m - 4, 8, 8}, 2, th.TitleBar)

	c.TextIn(Rect{th.Pad + 30, 0, c.W / 2, th.TitleH}, w.Title, th.TextOnCK, th.FontTitle, -1)
	if w.Subtitle != "" {
		r := Rect{c.W/2 - th.Pad, 0, c.W/2 - th.Pad, th.TitleH}
		c.TextIn(r, w.Subtitle, Blend(th.TitleBar, th.TextOnCK, 150), th.FontSmall, 1)
	}
}

func (w *Wizard) drawSidebar() {
	th := w.theme
	c := w.canvas
	bar := Rect{0, th.TitleH, th.SidebarW, c.H - th.TitleH}
	c.Fill(bar, th.Sidebar)

	// The vertical line joining the steps. The part already walked stays bright, to
	// show the progress.
	//
	// 단계들을 잇는 세로 선. 지나온 구간은 밝게 남겨 진행을 보여준다.
	const badge = 30
	cx := th.Pad + badge/2
	rowH := th.RowH + 14
	top := th.TitleH + th.Pad + rowH/2

	for i := range w.steps {
		if i == len(w.steps)-1 {
			break
		}
		y0 := top + i*rowH + badge/2
		y1 := top + (i+1)*rowH - badge/2
		col := Blend(th.Sidebar, th.TextOnCK, 40)
		if i < w.cur {
			col = th.Accent
		}
		c.Fill(Rect{cx - 1, y0, 2, y1 - y0}, col)
	}

	for i, st := range w.steps {
		cy := top + i*rowH
		r := Rect{0, cy - rowH/2, th.SidebarW, rowH}

		// The badge and text colours differ between a step already done, the current
		// step, and one not reached yet.
		//
		// 배지와 글자 색은 지나온 단계 / 지금 단계 / 아직 안 온 단계로 갈린다.
		var fill, mark, label Color
		switch {
		case i < w.cur:
			fill, mark, label = th.Accent, th.TextOnCK, Blend(th.Sidebar, th.TextOnCK, 200)
		case i == w.cur:
			fill, mark, label = th.TextOnCK, th.Sidebar, th.TextOnCK
			c.Fill(Rect{0, r.Y, 3, r.H}, th.Accent)
		default:
			fill = Blend(th.Sidebar, th.TextOnCK, 30)
			mark = Blend(th.Sidebar, th.TextOnCK, 130)
			label = Blend(th.Sidebar, th.TextOnCK, 120)
		}

		c.Circle(cx, cy, badge/2, fill)
		if i < w.cur {
			w.drawCheck(Rect{cx - badge/2, cy - badge/2, badge, badge}, mark)
		} else {
			c.TextIn(Rect{cx - badge/2, cy - badge/2, badge, badge}, itoa(i+1), mark, th.FontSmall, 0)
		}

		textR := Rect{cx + badge/2 + 14, cy - rowH/2, th.SidebarW - cx - badge/2 - 20, rowH}
		c.TextIn(textR, th.FontBody.Truncate(st.Title, textR.W), label, th.FontBody, -1)
	}
}

// drawCheck is the done mark: two strokes of a tick, centred in the rectangle.
//
// The strokes are 3 pixels thick. At 2 they smear into something that reads as
// an X inside the badge.
//
// drawCheck - 완료 표시. 주어진 사각형 가운데에 체크 두 획을 그린다.
//
// 획을 3 픽셀 두께로 긋는다. 2 픽셀이면 배지 안에서 X 처럼 뭉개져 보인다.
func (w *Wizard) drawCheck(r Rect, col Color) {
	c := w.canvas
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	// The short stroke down to the left.
	// 짧은 왼쪽 아래 획.
	for i := 0; i < 4; i++ {
		c.Fill(Rect{cx - 6 + i, cy - 1 + i, 3, 3}, col)
	}
	// The long stroke up to the right.
	// 긴 오른쪽 위 획.
	for i := 0; i < 7; i++ {
		c.Fill(Rect{cx - 3 + i, cy + 2 - i, 3, 3}, col)
	}
}

func (w *Wizard) drawHeading() {
	th := w.theme
	c := w.canvas
	st := w.steps[w.cur]

	head := st.Heading
	if head == "" {
		head = st.Title
	}
	x := th.SidebarW + th.Pad*2
	width := c.W - x - th.Pad

	y := th.TitleH + th.Pad
	c.TextIn(Rect{x, y, width, th.FontHead.Height()}, head, th.Text, th.FontHead, -1)
	if st.Subtitle != "" {
		c.TextIn(Rect{x, y + th.FontHead.Height() + 4, width, th.FontSmall.Height()},
			th.FontSmall.Truncate(st.Subtitle, width), th.TextDim, th.FontSmall, -1)
	}
	// The faint line parting the title from the body.
	// 제목과 본문을 가르는 옅은 선.
	c.HLine(x, y+th.FontHead.Height()+th.FontSmall.Height()+th.Pad, width,
		Blend(th.Panel, th.Border, 140))
}

func (w *Wizard) drawButtonBar() {
	th := w.theme
	c := w.canvas
	bar := Rect{0, c.H - th.ButtonBarH, c.W, th.ButtonBarH}
	c.Fill(bar, th.ButtonBar)
	c.HLine(0, bar.Y, c.W, Blend(th.ButtonBar, th.Border, 170))

	if w.status != "" {
		col := th.TextDim
		if w.statusErr {
			col = th.Danger
		}
		r := Rect{th.Pad, bar.Y, c.W - 420, th.ButtonBarH}
		c.TextIn(r, th.FontBody.Truncate(w.status, r.W), col, th.FontBody, -1)
	}

	st := w.steps[w.cur]
	if !st.HideBack {
		w.backBtn.Draw(c, th)
	}
	if !st.HideNext {
		w.nextBtn.Draw(c, th)
	}
	if w.OnCancel != nil {
		w.cancelBtn.Draw(c, th)
	}
}
