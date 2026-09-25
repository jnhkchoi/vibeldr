package image

import (
	"bytes"
	"strings"
	"testing"
)

func TestBootstrapGrubConfigShape(t *testing.T) {
	cfg := BootstrapGrubConfig()
	if len(cfg) == 0 {
		t.Fatal("BootstrapGrubConfig 가 빈 바이트를 돌려줌")
	}
	s := string(cfg)

	// The default has to be build, so a bootstrap image starts its own flow with
	// nobody pressing anything. A wrong default stops the boot on a headless VM.
	//
	// 기본값이 build 로 잡혀야 부트스트랩 이미지가 아무도 안 눌러도 자기
	// 흐름을 시작한다. 잘못된 기본값은 헤드리스 VM 에서 부팅이 멈춘다.
	if !strings.Contains(s, "set default="+BootstrapEntryID) {
		t.Errorf("default entry 지정 없음:\n%s", s)
	}
	if !strings.Contains(s, "menuentry 'vibeldr - configure and install' --id "+BootstrapEntryID) {
		t.Errorf("build 메뉴엔트리 없음:\n%s", s)
	}
	// The kernel and initrd lines are easy to let drift when the name is
	// hard-coded in two different places, so this catches whether the constant and
	// the actual text travel together.
	//
	// 커널·initrd 라인은 이름이 다른 두 곳에서 하드코딩되면 어긋나기
	// 쉬우므로, 여기서 상수와 실제 텍스트가 같이 흘러가는지를 잡는다.
	if !strings.Contains(s, "linux /"+BootstrapKernelName+" ") {
		t.Errorf("linux 라인이 %s 를 로드하지 않음:\n%s", BootstrapKernelName, s)
	}
	if !strings.Contains(s, "initrd /"+BootstrapInitrdName) {
		t.Errorf("initrd 라인이 %s 를 로드하지 않음:\n%s", BootstrapInitrdName, s)
	}
	// A serial console has to be secured for a headless VM to be diagnosable.
	// 시리얼 콘솔이 확보되어야 헤드리스 VM 진단이 가능하다.
	if !strings.Contains(s, "console=ttyS0,115200n8") {
		t.Errorf("시리얼 콘솔 파라미터 없음:\n%s", s)
	}
}

func TestBootstrapGrubConfigDeterministic(t *testing.T) {
	// Calling the same function twice has to give byte-identical output. A
	// reproducible build is the only thing that makes an image checksum mean
	// anything.
	//
	// 같은 함수를 두 번 부르면 바이트가 완전히 같아야 한다. 재현 가능한
	// 빌드가 이미지 checksum 을 의미 있게 만드는 유일한 방법이다.
	a := BootstrapGrubConfig()
	b := BootstrapGrubConfig()
	if !bytes.Equal(a, b) {
		t.Errorf("BootstrapGrubConfig 이 결정적이지 않음")
	}
}
