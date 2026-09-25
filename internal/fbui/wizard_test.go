// wizard_test.go actually draws the wizard screens and writes them out as PNGs.
//
// The layout has to be inspectable where there is no framebuffer, so each step
// is filled with representative data and saved under testdata. The value checks
// alongside are whether the widgets landed inside the screen and whether a
// click really changes a value.
//
// wizard_test.go - 마법사 화면을 실제로 그려 PNG 로 떠 두는 테스트.
//
// 프레임버퍼가 없는 곳에서도 레이아웃을 눈으로 확인할 수 있어야 해서,
// 각 단계를 대표 데이터로 채워 testdata 아래에 저장한다. 함께 하는 값
// 검사는 위젯이 화면 안에 놓였는지와 클릭이 실제로 값을 바꾸는지다.
package fbui

import (
	"testing"
)

// mockSteps is the install wizard's step layout, with representative values in
// place of real data.
//
// mockSteps - 설치 마법사의 단계 구성. 실제 데이터 대신 대표값을 쓴다.
func mockSteps() []Step {
	models := []string{"DS918+", "DS3622xs+", "SA6400"}
	bays := []string{"사용 안 함", "1번 베이", "2번 베이", "3번 베이", "4번 베이",
		"5번 베이", "6번 베이", "7번 베이", "8번 베이"}
	ifaces := []string{"eth0 (첫 번째)", "eth1", "eth2", "eth3"}

	return []Step{
		{
			Title:    "시작",
			Heading:  "vibeldr 설치 마법사",
			Subtitle: "시작하기 전에 확인하세요.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				for _, line := range []string{
					"이 마법사는 시놀로지 DSM 을 이 컴퓨터에서 돌리기 위한 부트로더를 설치합니다.",
					"흉내낼 기종을 고르면, 이 컴퓨터의 디스크와 랜카드를 그 기종에 맞게 배치하고",
					"DSM 7.4 를 내려받아 이 컴퓨터에 맞게 고친 뒤 부트 디스크에 씁니다.",
				} {
					f.Text(line, false)
				}
				f.Space(th.Pad)
				rows := []CardRow{
					{Label: "디스크 컨트롤러", Value: "2 개"},
					{Value: "0000:00:1f.2  AHCI  6 포트", Dim: true},
					{Value: "0000:06:07.0  AHCI  6 포트", Dim: true},
					{Label: "디스크", Value: "3 개"},
					{Value: "QEMU HARDDISK  34.4 GB  (sata1)", Dim: true},
					{Value: "QEMU HARDDISK  34.4 GB  (sata2)", Dim: true},
					{Value: "Samsung SSD 870 EVO  1.0 TB  (sata3)", Dim: true},
					{Label: "랜카드", Value: "2 개"},
					{Value: "eth0  virtio_net  BC:24:11:3A:7F:02", Dim: true},
					{Value: "eth1  e1000e  BC:24:11:9C:41:88", Dim: true},
				}
				card := NewCard("감지된 시스템", rows...)
				card.LabelW = 190
				f.Full(card, CardHeight(th, "감지된 시스템", len(rows)))
				f.Space(th.Pad)
				f.Text("계속하려면 다음을 누르세요.", true)
				p.Add(f.Widgets()...)
			},
		},
		{
			Title:    "기종",
			Heading:  "모델 선택",
			Subtitle: "이 컴퓨터를 어떤 시놀로지 모델로 인식시킬지 고릅니다.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				f.LabelW = 150
				f.RowWidth("모델", NewDropdown(models, 2, nil), 320, 0)
				f.Space(th.Pad)
				rows := []CardRow{
					{Label: "플랫폼", Value: "epyc7002"},
					{Label: "커널", Value: "5.10.55"},
					{Label: "설치할 DSM", Value: "7.4.1-90080", Accent: true},
					{Label: "시리얼 규칙", Value: "있음"},
				}
				f.Full(NewCard("SA6400", rows...), CardHeight(th, "SA6400", len(rows)))
				f.Space(th.Pad)
				f.Text("DSM 이미지는 설치 단계에서 시놀로지 서버로부터 내려받습니다.", true)
				p.Add(f.Widgets()...)
			},
		},
		{
			Title:    "부트 디스크",
			Heading:  "부트로더 디스크",
			Subtitle: "로더가 설치된 디스크입니다. DSM 은 이 디스크를 저장소로 쓰지 않습니다.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				f.RowWidth("부트 디스크", NewDropdown([]string{
					"sda - QEMU HARDDISK (1.0 GB)  [VIBELDR1 발견]",
					"sdb - QEMU HARDDISK (32 GB)",
				}, 0, nil), 460, 0)
				f.Space(10)
				f.Separator()
				f.Text("파티션 구성", false)
				f.Space(4)
				f.LabelW = 200
				f.Row("파티션 1", roLabel("VIBELDR1  FAT16  256 MB  - 로더 커널과 설정"), 0)
				f.Row("파티션 2", roLabel("VIBELDR2  FAT16   32 MB  - 예비"), 0)
				f.Row("파티션 3", roLabel("VIBELDR3  FAT32  700 MB  - DSM 커널과 램디스크"), 0)
				p.Add(f.Widgets()...)
			},
		},
		{
			Title:    "저장소",
			Heading:  "디스크를 베이에 배치",
			Subtitle: "감지된 SATA 컨트롤러와 디스크를 모델의 베이 번호에 맞춥니다.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				f.Text("컨트롤러  0000:00:1f.2  AHCI  (6 포트)", false)
				f.Space(4)
				f.Columns(26, []int{3, 5, 3, 4}, []Widget{
					hdr("포트"), hdr("디스크"), hdr("용량"), hdr("배치"),
				})
				f.Separator()
				disks := []struct {
					port, name, size string
					bay              int
				}{
					{"ata1", "QEMU HARDDISK", "32 GB", 1},
					{"ata2", "QEMU HARDDISK", "32 GB", 2},
					{"ata3", "Samsung SSD 870 EVO", "1.0 TB", 3},
					{"ata4", "(비어 있음)", "-", 0},
				}
				for _, d := range disks {
					f.Columns(th.RowH, []int{3, 5, 3, 4}, []Widget{
						roLabel(d.port), roLabel(d.name), roLabel(d.size),
						NewDropdown(bays, d.bay, nil),
					})
				}
				f.Space(6)
				f.Text("베이 번호는 DSM 저장소 관리자에 보이는 순서입니다. 비어 있는 포트는 사용 안 함으로 두세요.", true)
				p.Add(f.Widgets()...)
			},
		},
		{
			Title:    "네트워크",
			Heading:  "랜카드 순서와 MAC 주소",
			Subtitle: "감지된 랜카드를 DSM 의 eth 번호에 맞추고 MAC 주소를 정합니다.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				f.Columns(26, []int{4, 4, 4, 5}, []Widget{
					hdr("장치"), hdr("드라이버"), hdr("실제 MAC"), hdr("DSM 인터페이스"),
				})
				f.Separator()
				nics := []struct {
					dev, drv, mac string
					idx           int
				}{
					{"enp0s18", "virtio_net", "BC:24:11:3A:7F:02", 0},
					{"enp0s19", "e1000e", "BC:24:11:9C:41:88", 1},
				}
				for _, n := range nics {
					f.Columns(th.RowH, []int{4, 4, 4, 5}, []Widget{
						roLabel(n.dev), roLabel(n.drv), roLabel(n.mac),
						NewDropdown(ifaces, n.idx, nil),
					})
				}
				f.Space(10)
				f.Separator()
				f.LabelW = 200
				f.Full(NewRadioGroup([]string{
					"실제 MAC 주소를 그대로 사용 (권장)",
					"시리얼에서 생성한 MAC 주소 사용",
				}, 0, nil), th.RowH*2)
				f.Space(6)
				f.Text("MAC 주소를 바꾸면 공유기의 DHCP 예약과 DSM 라이선스가 새 주소를 따라갑니다.", true)
				p.Add(f.Widgets()...)
			},
		},
		{
			Title:    "인증 정보",
			Heading:  "시리얼 번호",
			Subtitle: "고른 모델의 규칙에 맞는 시리얼을 만들거나 직접 넣습니다.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				f.Full(NewRadioGroup([]string{
					"자동 생성 (모델 규칙에 맞춰 만듭니다)",
					"직접 입력",
				}, 0, nil), th.RowH*2)
				f.Space(10)
				f.LabelW = 200
				sn := NewTextField("2270SQRLR3H9M", nil)
				sn.Upper = true
				sn.MaxLen = 13
				f.RowWidth("시리얼 번호", sn, 300, 0)
				f.RowWidth("MAC 접두", NewTextField("00:11:32", nil), 300, 0)
				f.Space(8)
				f.Text("SA6400 규칙: 앞 4자리는 생산 주차, 가운데 6자리는 영문·숫자, 끝 3자리는 영문.", true)
				p.Add(f.Widgets()...)
			},
		},
		{
			Title:    "확인",
			Heading:  "설정 확인",
			Subtitle: "아래 내용으로 로더를 만듭니다. 고치려면 뒤로 가세요.",
			Build: func(w *Wizard, p *Panel, body Rect) {
				th := w.Theme()
				f := NewForm(body, th)
				r1 := []CardRow{
					{Label: "모델", Value: "SA6400  (epyc7002)"},
					{Label: "설치할 DSM", Value: "7.4.1-90080", Accent: true},
					{Label: "시리얼", Value: "2270SQRLR3H9M"},
				}
				f.Full(NewCard("모델", r1...), CardHeight(th, "모델", len(r1)))
				f.Space(th.Pad)
				r2 := []CardRow{
					{Label: "부트 디스크", Value: "/dev/sda  1.0 GB"},
					{Label: "저장소", Value: "3 대 - 베이 1, 2, 3"},
					{Label: "네트워크", Value: "enp0s18 → eth0,  enp0s19 → eth1"},
				}
				f.Full(NewCard("이 컴퓨터", r2...), CardHeight(th, "이 컴퓨터", len(r2)))
				f.Space(th.Pad)
				f.Text("다음을 누르면 DSM 이미지를 내려받아 패치하고 부트 디스크에 씁니다.", true)
				p.Add(f.Widgets()...)
			},
			NextLabel: "설치 시작",
		},
		{
			Title:    "설치",
			Heading:  "설치 중",
			Subtitle: "완료될 때까지 전원을 끄지 마세요.",
			HideBack: true,
			HideNext: true,
			Build: func(w *Wizard, p *Panel, body Rect) {
				f := NewForm(body, w.Theme())
				bar := &ProgressBar{Value: 0.62}
				f.Full(bar, 28)
				f.Space(6)
				f.Text("DSM 커널 패치 중 - 서명 검사 우회 적용", false)
				f.Space(14)
				f.Separator()
				for _, line := range []string{
					"[완료] DSM 이미지 내려받기  (460 MB)",
					"[완료] 이미지 복호화",
					"[완료] 램디스크 풀기",
					"[완료] 드라이버 42 개 넣기",
					"[진행] 커널 패치",
					"[대기] 부트 디스크에 쓰기",
					"[대기] 부트 메뉴 갱신",
				} {
					f.Text(line, true)
				}
				p.Add(f.Widgets()...)
			},
		},
	}
}

// roLabel is the label that only displays a value.
// roLabel - 읽기 전용 값 표시용 라벨.
func roLabel(s string) *Label { return NewLabel(s) }

// hdr is a list heading label.
// hdr - 목록 머리글 라벨.
func hdr(s string) *Label {
	l := NewLabel(s)
	l.Dim = true
	return l
}

// TestWizardScreens draws every step, saves them as PNGs and checks the widgets
// landed inside the screen.
//
// TestWizardScreens - 모든 단계를 그려 PNG 로 저장하고, 위젯이 화면 안에
// 놓였는지 확인한다.
func TestWizardScreens(t *testing.T) {
	steps := mockSteps()
	c := NewCanvas(1024, 768)
	th := DefaultTheme()
	w := NewWizard(c, th, "vibeldr", steps)
	w.Subtitle = "DSM 부트로더"
	w.OnCancel = func() {}

	names := []string{
		"01-start", "02-model", "03-bootdisk", "04-storage",
		"05-network", "06-identity", "07-confirm", "08-install",
	}
	for i := range steps {
		w.GoTo(i)
		w.Draw()
		dumpPNG(t, c, "wizard-"+names[i]+".png")

		for _, wd := range w.Body().Widgets() {
			b := wd.Bounds()
			if b.W <= 0 || b.H <= 0 {
				t.Errorf("단계 %d: 크기가 0 인 위젯 %T", i, wd)
			}
			if b.Y+b.H > c.H-th.ButtonBarH {
				t.Errorf("단계 %d: 위젯 %T 이 버튼 막대를 침범 (아래 끝 %d)", i, wd, b.Y+b.H)
			}
			if b.X < th.SidebarW {
				t.Errorf("단계 %d: 위젯 %T 이 사이드바를 침범 (왼쪽 끝 %d)", i, wd, b.X)
			}
		}
	}
}

// TestDropdownOpenRendersAbovePanel: whether an open list is drawn over the
// other widgets, and whether pressing an item changes the value.
//
// TestDropdownOpenRendersAbovePanel - 펼친 목록이 다른 위젯 위에 그려지고,
// 항목을 누르면 값이 바뀌는지 본다.
func TestDropdownOpenRendersAbovePanel(t *testing.T) {
	c := NewCanvas(1024, 768)
	th := DefaultTheme()

	got := -1
	dd := NewDropdown([]string{"DS918+", "DS3622xs+", "SA6400"}, 0, func(i int) { got = i })
	steps := []Step{{
		Title: "모델",
		Build: func(w *Wizard, p *Panel, body Rect) {
			f := NewForm(body, th)
			f.RowWidth("모델", dd, 280, 0)
			// Another control goes right below the dropdown, so it can be seen whether the
			// open list covers it.
			//
			// 드롭다운 바로 아래에 다른 컨트롤을 둬서, 펼친 목록이 그것을
			// 덮는지 확인할 수 있게 한다.
			f.RowWidth("DSM 버전", NewDropdown([]string{"7.4.1-90080"}, 0, nil), 280, 0)
			p.Add(f.Widgets()...)
		},
	}}
	w := NewWizard(c, th, "vibeldr", steps)

	b := dd.Bounds()
	w.Handle(Event{Kind: EventMouseDown, X: b.X + 5, Y: b.Y + 5})
	w.Handle(Event{Kind: EventMouseUp, X: b.X + 5, Y: b.Y + 5})
	if !dd.open {
		t.Fatal("클릭했는데 목록이 안 열림")
	}
	w.Draw()
	dumpPNG(t, c, "wizard-dropdown-open.png")

	// The vertical position of the third item.
	// 세 번째 항목의 세로 위치.
	pr := dd.PopupBounds()
	y := pr.Y + 1 + th.RowH*2 + th.RowH/2
	w.Handle(Event{Kind: EventMouseMove, X: pr.X + 10, Y: y})
	w.Handle(Event{Kind: EventMouseUp, X: pr.X + 10, Y: y})

	if dd.open {
		t.Error("항목을 골랐는데 목록이 안 닫힘")
	}
	if got != 2 || dd.Selected != 2 {
		t.Errorf("세 번째 항목을 골랐는데 OnChange=%d Selected=%d", got, dd.Selected)
	}
	if dd.Value() != "SA6400" {
		t.Errorf("값이 %q", dd.Value())
	}
}

// TestWizardNextValidationBlocks: whether a failed validation stops the step
// moving on and leaves the message.
//
// TestWizardNextValidationBlocks - 검사가 실패하면 단계가 안 넘어가고
// 메시지가 남는지 본다.
func TestWizardNextValidationBlocks(t *testing.T) {
	c := NewCanvas(1024, 768)
	blocked := "모델을 먼저 고르세요"
	allow := false
	steps := []Step{
		{Title: "1", Validate: func() string {
			if allow {
				return ""
			}
			return blocked
		}},
		{Title: "2"},
	}
	w := NewWizard(c, DefaultTheme(), "vibeldr", steps)

	w.Next()
	if w.Step() != 0 {
		t.Fatalf("검사가 막았는데 단계가 %d 로 넘어감", w.Step())
	}
	if w.status != blocked || !w.statusErr {
		t.Errorf("오류 메시지가 %q (err=%v)", w.status, w.statusErr)
	}

	allow = true
	w.Next()
	if w.Step() != 1 {
		t.Errorf("검사를 통과했는데 단계가 %d", w.Step())
	}
	if w.status != "" {
		t.Errorf("단계를 넘어갔는데 메시지가 남음: %q", w.status)
	}
}

// TestTextFieldTyping covers typing and deleting, the filter and the maximum
// length.
//
// TestTextFieldTyping - 글자 입력과 지우기, 필터와 최대 길이.
func TestTextFieldTyping(t *testing.T) {
	var last string
	f := NewTextField("", func(s string) { last = s })
	f.Upper = true
	f.MaxLen = 4
	f.Filter = func(r rune) bool {
		return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
	}
	f.SetFocused(true)
	p := NewPanel(nil)
	p.Add(f)

	for _, r := range "ab-12345" {
		p.Handle(Event{Kind: EventKey, Rune: r})
	}
	if f.Text() != "AB12" {
		t.Errorf("입력 결과가 %q (대문자 변환·필터·최대 길이)", f.Text())
	}
	if last != "AB12" {
		t.Errorf("OnChange 마지막 값이 %q", last)
	}

	p.Handle(Event{Kind: EventKey, Key: KeyBackspace})
	if f.Text() != "AB1" {
		t.Errorf("백스페이스 뒤 %q", f.Text())
	}
	p.Handle(Event{Kind: EventKey, Key: KeyHome})
	p.Handle(Event{Kind: EventKey, Key: KeyDelete})
	if f.Text() != "B1" {
		t.Errorf("맨 앞 글자를 지운 뒤 %q", f.Text())
	}
}

// TestPanelTabOrder: whether Tab cycles only the focusable widgets and skips
// the disabled ones.
//
// TestPanelTabOrder - 탭이 포커스 가능한 위젯만 돌고, 비활성은 건너뛰는지 본다.
func TestPanelTabOrder(t *testing.T) {
	p := NewPanel(nil)
	l := NewLabel("설명")
	b1 := NewButton("하나", nil)
	b2 := NewButton("둘", nil)
	b3 := NewButton("셋", nil)
	b2.SetDisabled(true)
	p.Add(l, b1, b2, b3)

	p.FocusFirst()
	if p.widgets[p.focus] != Widget(b1) {
		t.Fatalf("첫 포커스가 %T", p.widgets[p.focus])
	}
	p.Handle(Event{Kind: EventKey, Key: KeyTab})
	if p.widgets[p.focus] != Widget(b3) {
		t.Errorf("비활성 위젯을 건너뛰지 않음: 지금 %T", p.widgets[p.focus])
	}
	p.Handle(Event{Kind: EventKey, Key: KeyTab})
	if p.widgets[p.focus] != Widget(b1) {
		t.Errorf("끝에서 처음으로 안 돌아옴: 지금 %T", p.widgets[p.focus])
	}
	p.Handle(Event{Kind: EventKey, Key: KeyShiftTab})
	if p.widgets[p.focus] != Widget(b3) {
		t.Errorf("역방향 탭이 틀림: 지금 %T", p.widgets[p.focus])
	}
}

// TestButtonClickFires: whether pressing and releasing fires the action, and
// releasing outside does not.
//
// TestButtonClickFires - 누르고 떼면 동작이 일어나고, 밖에서 떼면 안
// 일어나는지 본다.
func TestButtonClickFires(t *testing.T) {
	n := 0
	b := NewButton("설치", func() { n++ })
	b.SetBounds(Rect{100, 100, 120, 38})
	p := NewPanel(nil)
	p.Add(b)

	p.Handle(Event{Kind: EventMouseDown, X: 110, Y: 110})
	p.Handle(Event{Kind: EventMouseUp, X: 110, Y: 110})
	if n != 1 {
		t.Fatalf("클릭 뒤 호출 횟수 %d", n)
	}

	// Pressing, moving off the button and releasing cancels it.
	// 눌렀다가 버튼 밖으로 나가서 떼면 취소된다.
	p.Handle(Event{Kind: EventMouseDown, X: 110, Y: 110})
	p.Handle(Event{Kind: EventMouseUp, X: 500, Y: 500})
	if n != 1 {
		t.Errorf("밖에서 뗐는데 호출됨: %d", n)
	}
}

// TestEnterAdvancesPastDropdown: Enter has to move to the next step even with
// focus on a dropdown. A closed dropdown intercepting Enter makes the wizard
// impossible to advance without a mouse.
//
// TestEnterAdvancesPastDropdown - 포커스가 드롭다운에 있어도 엔터는 다음
// 단계로 가야 한다. 닫힌 드롭다운이 엔터를 가로채면 마우스 없이는 마법사를
// 진행할 수 없다.
func TestEnterAdvancesPastDropdown(t *testing.T) {
	c := NewCanvas(1024, 768)
	dd := NewDropdown([]string{"DS918+", "SA6400"}, 0, nil)
	steps := []Step{
		{Title: "모델", Build: func(w *Wizard, p *Panel, body Rect) {
			f := NewForm(body, w.Theme())
			f.RowWidth("모델", dd, 280, 0)
			p.Add(f.Widgets()...)
		}},
		{Title: "다음 단계"},
	}
	w := NewWizard(c, DefaultTheme(), "vibeldr", steps)

	if !dd.focused {
		t.Fatal("첫 포커스가 드롭다운에 있어야 함")
	}
	w.Handle(Event{Kind: EventKey, Key: KeyEnter})
	if w.Step() != 1 {
		t.Errorf("엔터로 넘어가지 못함: 단계 %d, 드롭다운 열림=%v", w.Step(), dd.open)
	}
}

// TestSpaceOpensDropdown: Enter is passed through, so Space has to be what
// opens it.
//
// TestSpaceOpensDropdown - 엔터를 넘긴 대신 스페이스로는 열려야 한다.
func TestSpaceOpensDropdown(t *testing.T) {
	c := NewCanvas(1024, 768)
	dd := NewDropdown([]string{"DS918+", "SA6400"}, 0, nil)
	steps := []Step{{Title: "모델", Build: func(w *Wizard, p *Panel, body Rect) {
		f := NewForm(body, w.Theme())
		f.RowWidth("모델", dd, 280, 0)
		p.Add(f.Widgets()...)
	}}}
	w := NewWizard(c, DefaultTheme(), "vibeldr", steps)

	w.Handle(Event{Kind: EventKey, Key: KeySpace})
	if !dd.open {
		t.Error("스페이스로 목록이 열려야 함")
	}
	w.Handle(Event{Kind: EventKey, Key: KeyEscape})
	if dd.open {
		t.Error("ESC 로 목록이 닫혀야 함")
	}
}

// TestTabReachesButtonBar: whether Tab carries on past the body and into the
// button bar. With the two panels cycling separately, the Next button is
// unreachable without a mouse.
//
// TestTabReachesButtonBar - 탭이 본문을 지나 버튼 막대까지 이어지는지 본다.
// 두 패널이 각자 돌면 마우스 없이는 다음 버튼에 닿을 수 없다.
func TestTabReachesButtonBar(t *testing.T) {
	c := NewCanvas(1024, 768)
	dd := NewDropdown([]string{"a", "b"}, 0, nil)
	steps := []Step{
		{Title: "1", Build: func(w *Wizard, p *Panel, body Rect) {
			f := NewForm(body, w.Theme())
			f.RowWidth("값", dd, 280, 0)
			p.Add(f.Widgets()...)
		}},
		{Title: "2"},
	}
	w := NewWizard(c, DefaultTheme(), "vibeldr", steps)
	w.OnCancel = func() {}

	// The body has only one focusable widget, the dropdown. A few presses of Tab
	// have to cross into the button bar.
	//
	// 본문에는 포커스 가능한 위젯이 드롭다운 하나뿐이다. 탭을 몇 번
	// 누르면 버튼 막대로 넘어가야 한다.
	reached := false
	for i := 0; i < 6; i++ {
		w.Handle(Event{Kind: EventKey, Key: KeyTab})
		if w.chrome.HasFocus() {
			reached = true
			break
		}
	}
	if !reached {
		t.Fatal("탭으로 버튼 막대에 닿지 못함")
	}

	// Continuing to press Tab on the button bar has to come back to the body.
	// 버튼 막대에서 계속 탭을 누르면 본문으로 돌아와야 한다.
	back := false
	for i := 0; i < 6; i++ {
		w.Handle(Event{Kind: EventKey, Key: KeyTab})
		if !w.chrome.HasFocus() && w.body.HasFocus() {
			back = true
			break
		}
	}
	if !back {
		t.Error("탭이 본문으로 돌아오지 않음")
	}
}

// TestRebuildBodyRefreshesCard: whether changing the dropdown redraws the card
// on the same screen with the new value. Without RebuildBody the card holds the
// old value - the model dropdown says 3622 while the card still says 6400 - and
// this test catches that.
//
// TestRebuildBodyRefreshesCard - 드롭다운을 바꾸면 같은 화면의 카드가 새
// 값으로 다시 그려지는지 본다. RebuildBody 가 빠지면 카드가 옛 값을 그대로
// 들고 있어(모델 드롭다운은 3622인데 카드는 6400) 이 테스트가 잡는다.
func TestRebuildBodyRefreshesCard(t *testing.T) {
	c := NewCanvas(1024, 768)
	models := []string{"DS918+", "DS3622xs+", "SA6400"}
	// A screen where the card's value follows the choice; a scaled-down buildModel.
	// 선택에 따라 카드 값이 바뀌는 화면. 실제 buildModel 의 축소판.
	steps := []Step{{
		Title: "모델",
		Build: func(w *Wizard, p *Panel, body Rect) {
			f := NewForm(body, w.Theme())
			dd := NewDropdown(models, w.pick, func(i int) {
				w.pick = i
				w.RebuildBody()
			})
			f.RowWidth("모델", dd, 280, 0)
			f.Full(NewCard("정보", CardRow{Label: "모델", Value: models[w.pick]}),
				CardHeight(w.Theme(), "정보", 1))
			p.Add(f.Widgets()...)
		},
	}}
	w := NewWizard(c, DefaultTheme(), "vibeldr", steps)

	// Find the dropdown in the body by type - the form puts a label in first, so
	// the index may not be 0.
	//
	// 본문에서 드롭다운을 타입으로 찾는다 (폼이 라벨을 먼저 넣어 인덱스가
	// 0 이 아닐 수 있다).
	var dd *Dropdown
	for _, wd := range w.body.Widgets() {
		if d, ok := wd.(*Dropdown); ok {
			dd = d
			break
		}
	}
	if dd == nil {
		t.Fatal("드롭다운을 찾지 못함")
	}
	// Call the callback as though the second item, DS3622xs+, had been chosen.
	// 두 번째 항목(DS3622xs+) 을 고른 것처럼 콜백을 부른다.
	dd.Select(1)
	dd.OnChange(1)

	// Find the card widget and see whether its value changed to DS3622xs+.
	// 카드 위젯을 찾아 값이 DS3622xs+ 로 바뀌었는지 본다.
	var card *Card
	for _, wd := range w.body.Widgets() {
		if cc, ok := wd.(*Card); ok {
			card = cc
		}
	}
	if card == nil {
		t.Fatal("카드를 찾지 못함")
	}
	if got := card.Rows[0].Value; got != "DS3622xs+" {
		t.Errorf("카드가 안 바뀜: %q (RebuildBody 가 카드를 다시 안 지었다)", got)
	}
}

// TestBuildCanLockNext: a step that locks the Next button while it builds - the
// install screen does - keeps it locked. Entering the step must not unlock it
// again after Build, or the reboot button looks usable mid-install.
//
// TestBuildCanLockNext - 짓는 동안 다음 버튼을 잠그는 단계(설치 화면)는 잠긴
// 채로 남아야 한다. 단계 진입이 Build 뒤에 다시 풀면 설치 중에도 재부팅 버튼이
// 눌리는 것처럼 보인다.
func TestBuildCanLockNext(t *testing.T) {
	steps := []Step{
		{Title: "설치", NextLabel: "재부팅", Build: func(w *Wizard, p *Panel, body Rect) {
			w.SetNextEnabled(false)
		}},
	}
	w := NewWizard(NewCanvas(1024, 768), DefaultTheme(), "vibeldr", steps)
	if !w.nextBtn.Disabled() {
		t.Fatal("Build 가 잠근 다음 버튼이 단계 진입 뒤 다시 풀림")
	}
}
