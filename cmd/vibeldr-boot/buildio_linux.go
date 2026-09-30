//go:build linux

// buildio_linux.go is where the build pipeline talks to the user.
//
// buildFlow streams its progress to standard output and waits for the user only
// where it is stuck. That waiting works differently per kind of screen. A text
// screen takes one keypress; a graphical screen cannot read keys directly
// (another loop is already reading the input) and has to put a dialog up.
//
// So only the two waiting calls are behind an interface and the progress log
// stays on standard output. The graphical screen intercepts standard output and
// pours it into its log window.
//
// buildio_linux.go - 빌드 파이프라인이 사용자와 주고받는 부분.
//
// buildFlow 는 진행 상황을 표준 출력으로 흘리고, 막히는 지점에서만
// 사용자를 기다린다. 그 "기다리는" 동작이 화면 방식마다 다르다. 글자
// 화면에서는 키를 한 번 받으면 되지만, 그래픽 화면에서는 키를 직접 읽을
// 수 없고 (입력을 이미 다른 루프가 읽고 있다) 대화상자를 띄워야 한다.
//
// 그래서 기다리는 두 가지만 인터페이스로 빼고, 진행 로그는 그대로 표준
// 출력에 둔다. 그래픽 화면은 표준 출력을 가로채 로그 창에 뿌린다.
package main

// buildIO is the set of points a build has to wait for the user at.
// buildIO - 빌드 도중 사용자를 기다려야 하는 지점.
type buildIO interface {
	// Pause shows a message and waits for the user to acknowledge it. It is
	// usually called right before a failure ends the build.
	//
	// Pause - 메시지를 보여주고 사용자가 확인할 때까지 기다린다. 대개
	// 오류를 알리고 빌드를 접기 직전에 불린다.
	Pause(msg string)
	// Prompt takes one line of input; an empty answer comes back empty.
	// Prompt - 한 줄 입력을 받는다. 사용자가 비우면 빈 문자열.
	Prompt(label string) string
}

// textBuildIO is the text-screen version, using standard input and output.
// textBuildIO - 글자 화면용. 표준 입출력을 그대로 쓴다.
type textBuildIO struct{}

// Pause and Prompt hand straight over to the package's text helpers.
// Pause / Prompt - 이 패키지의 글자 화면 헬퍼로 그대로 넘긴다.
func (textBuildIO) Pause(msg string)           { pause(msg) }
func (textBuildIO) Prompt(label string) string { return prompt(label) }
