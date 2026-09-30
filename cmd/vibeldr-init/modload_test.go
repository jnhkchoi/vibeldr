package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/hwscan"
)

// modloadRoot lays down the two files fitModuleLoading edits, as DS3622xs+
// ships them (crc32c-intel) with an SA6400-style aesni-intel line added.
//
// modloadRoot - fitModuleLoading 이 고치는 두 파일을 DS3622xs+ 가 싣는 모양
// (crc32c-intel) 에 SA6400 식 aesni-intel 줄을 더해 깐다.
func modloadRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(cryptoModulesConf, "cbc\nmd5\ncryptd\ncrc32c-intel\naesni-intel\narc4\n")
	write("/etc/synoinfo.conf", "support_aesni_intel=\"yes\"\n")
	write("/etc.defaults/synoinfo.conf", "support_aesni_intel=\"yes\"\n")
	return root
}

func readRel(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// On a CPU with neither feature both modules go, AES-NI support is switched
// off in both synoinfo copies, and the drop-in is written. A second run
// changes nothing more.
//
// 두 기능이 다 없는 CPU 에서는 두 모듈이 빠지고, synoinfo 두 사본에서 AES-NI
// 지원이 꺼지고, 설정 조각이 써진다. 두 번째 실행은 더 바꾸는 것이 없다.
func TestFitModuleLoadingKVM64(t *testing.T) {
	root := modloadRoot(t)
	done := fitModuleLoading(root, hwscan.CPUFlags{"sse2": true})
	if len(done) != 2 || !strings.Contains(done[0], "crc32c-intel aesni-intel") {
		t.Fatalf("done %v", done)
	}
	conf := readRel(t, root, cryptoModulesConf)
	if strings.Contains(conf, "\ncrc32c-intel\n") || strings.Contains(conf, "\naesni-intel\n") || !strings.Contains(conf, "\ncryptd\n") {
		t.Errorf("crypto conf:\n%s", conf)
	}
	for _, rel := range synoinfoFiles {
		if got := readRel(t, root, rel); !strings.Contains(got, `support_aesni_intel="no"`) {
			t.Errorf("%s: %s", rel, got)
		}
	}
	if got := readRel(t, root, modulesLoadDropIn); got != modulesLoadDropInBody {
		t.Errorf("drop-in %q", got)
	}
	if again := fitModuleLoading(root, hwscan.CPUFlags{"sse2": true}); len(again) != 0 {
		t.Errorf("second run changed %v", again)
	}
}

// A CPU that has both features keeps the stock list and the AES-NI setting;
// only the drop-in is added.
//
// 두 기능을 다 가진 CPU 는 원래 목록과 AES-NI 설정을 그대로 두고, 설정 조각만
// 더한다.
func TestFitModuleLoadingCapableCPU(t *testing.T) {
	root := modloadRoot(t)
	before := readRel(t, root, cryptoModulesConf)
	done := fitModuleLoading(root, hwscan.CPUFlags{"sse4_2": true, "aes": true})
	if len(done) != 1 || done[0] != "systemd-modules-load failures ignored" {
		t.Fatalf("done %v", done)
	}
	if readRel(t, root, cryptoModulesConf) != before {
		t.Error("crypto conf changed")
	}
	if got := readRel(t, root, "/etc/synoinfo.conf"); !strings.Contains(got, `support_aesni_intel="yes"`) {
		t.Errorf("synoinfo changed: %s", got)
	}
}
