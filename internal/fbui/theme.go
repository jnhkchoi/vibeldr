// theme.go holds the colours and the measurements.
//
// Every colour, margin and font size the install wizard uses lives in one
// place. A widget never picks its own colour; it takes one from here.
//
// theme.go - 색과 치수.
//
// 설치 마법사 한 벌에 쓰이는 모든 색·여백·글꼴 크기를 한곳에 모은다.
// 위젯은 자기 색을 직접 정하지 않고 여기서만 가져간다.
package fbui

// Theme is the palette and the measurements every screen shares.
// Theme - 화면 전체가 공유하는 팔레트와 치수.
type Theme struct {
	// Backgrounds / 배경 계열.
	Desktop   Color // behind the window / 창 바깥 바탕
	Panel     Color // the body panel / 본문 패널
	Sidebar   Color // behind the step list / 단계 목록 배경
	TitleBar  Color // the title bar at the top / 상단 제목 막대
	ButtonBar Color // the button bar at the bottom / 하단 버튼 막대

	// Text / 글자 계열.
	Text     Color // ordinary text / 기본 글자
	TextDim  Color // explanatory and secondary text / 설명·보조 글자
	TextOnCK Color // text on a dark background / 짙은 배경 위 글자
	Accent   Color // emphasis: the current step, a chosen item / 강조 (현재 단계, 선택 항목)
	Danger   Color // warnings and errors / 경고·오류
	OK       Color // success / 성공

	// Controls / 컨트롤 계열.
	Control       Color // behind buttons and input fields / 버튼·입력칸 바탕
	ControlHover  Color // with the mouse over it / 마우스가 올라간 상태
	ControlActive Color // pressed / 눌린 상태
	ControlDim    Color // disabled / 비활성
	Border        Color // border / 테두리
	BorderFocus   Color // border of the focused control / 포커스된 테두리
	Selection     Color // the chosen row in a list / 목록에서 고른 행

	// Fonts / 글꼴.
	FontTitle *Face // the title at the top / 상단 제목
	FontHead  *Face // a step's heading / 단계 제목
	FontBody  *Face // body text and controls / 본문·컨트롤
	FontSmall *Face // explanations and footnotes / 설명·각주
	FontMono  *Face // logs; the same face, smaller / 로그 (같은 글꼴이지만 작게)

	// Measurements / 치수.
	SidebarW   int // width of the step list on the left / 왼쪽 단계 목록 폭
	TitleH     int // height of the title bar / 상단 제목 막대 높이
	ButtonBarH int // height of the button bar / 하단 버튼 막대 높이
	Pad        int // the default margin / 기본 여백
	RowH       int // height of one control row / 컨트롤 한 줄 높이
	Radius     int // corner radius of a control / 컨트롤 모서리 반지름
	CardRadius int // corner radius of a card / 카드 모서리 반지름
}

// DefaultTheme is a deep navy background under a light body panel. A
// framebuffer does no colour correction, so the contrast is generous.
//
// DefaultTheme - 짙은 남색 바탕에 밝은 본문 패널. 프레임버퍼는 색 보정이
// 없으니 대비를 넉넉히 준다.
func DefaultTheme() *Theme {
	return &Theme{
		Desktop:   RGB(0x0B1622),
		Panel:     RGB(0xF7F9FB),
		Sidebar:   RGB(0x16293D),
		TitleBar:  RGB(0x0F1E2D),
		ButtonBar: RGB(0xEDF1F5),

		Text:     RGB(0x16232F),
		TextDim:  RGB(0x6B7C8C),
		TextOnCK: RGB(0xF2F6FA),
		Accent:   RGB(0x2F80ED),
		Danger:   RGB(0xD64545),
		OK:       RGB(0x27AE60),

		Control:       RGB(0xFFFFFF),
		ControlHover:  RGB(0xF0F6FE),
		ControlActive: RGB(0xDCEAFB),
		ControlDim:    RGB(0xE3E8ED),
		Border:        RGB(0xC3CDD7),
		BorderFocus:   RGB(0x2F80ED),
		Selection:     RGB(0xDCEAFB),

		FontTitle: MustFace(26),
		FontHead:  MustFace(24),
		FontBody:  MustFace(18),
		FontSmall: MustFace(15),
		FontMono:  MustFace(14),

		SidebarW:   250,
		TitleH:     68,
		ButtonBarH: 78,
		Pad:        18,
		RowH:       36,
		Radius:     5,
		CardRadius: 8,
	}
}
