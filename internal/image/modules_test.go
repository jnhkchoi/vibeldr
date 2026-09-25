package image

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"vibeldr/internal/catalog"
	"vibeldr/internal/kmod"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

// fakeRelease serves a latest-release listing the way GitHub does: the
// assets given, and SHA256SUMS over them. badSum names an asset whose listed
// hash is wrong.
//
// fakeRelease - GitHub 처럼 최신 릴리스 목록을 내준다: 주어진 자산과, 그 위의
// SHA256SUMS. badSum 은 목록의 해시를 틀리게 적을 자산 이름이다.
type fakeRelease struct {
	srv       *httptest.Server
	downloads atomic.Int32
}

func newFakeRelease(t *testing.T, tag string, assets map[string][]byte, badSum string) *fakeRelease {
	t.Helper()
	f := &fakeRelease{}
	var sums strings.Builder
	for name, body := range assets {
		h := sha256Hex(body)
		if name == badSum {
			h = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&sums, "%s  %s\n", h, name)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		var list []string
		for name := range assets {
			list = append(list, fmt.Sprintf(`{"name":%q,"browser_download_url":"%s/dl/%s"}`, name, f.srv.URL, name))
		}
		list = append(list, fmt.Sprintf(`{"name":"SHA256SUMS","browser_download_url":"%s/dl/SHA256SUMS"}`, f.srv.URL))
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[%s]}`, tag, strings.Join(list, ","))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		if name == "SHA256SUMS" {
			fmt.Fprint(w, sums.String())
			return
		}
		f.downloads.Add(1)
		w.Write(assets[name])
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// samePack reports whether a and b hold the same entries with the same bytes.
// samePack - a 와 b 가 같은 항목을 같은 내용으로 갖는지.
func samePack(a, b kmod.Pack) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		if other, ok := b[name]; !ok || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}

func testPacks(t *testing.T) (drivers, firmware []byte) {
	t.Helper()
	d, err := ModulePackCPIO(kmod.Pack{"r8169.ko": []byte("module"), kmod.OriginalsList: []byte("")})
	if err != nil {
		t.Fatal(err)
	}
	fw, err := ModulePackCPIO(kmod.Pack{FirmwarePrefix + "rtl_nic/rtl8168h-2.fw": []byte("fw"), FirmwarePrefix + "WHENCE": []byte("w")})
	if err != nil {
		t.Fatal(err)
	}
	return d, fw
}

func TestFetchModulePack(t *testing.T) {
	name := catalog.ModulePackName("apollolake", "DS918+")
	fwName := catalog.ModuleFirmwareName("apollolake", "DS918+")
	if name != "apollolake-DS918+.cpio.gz" || fwName != "apollolake-DS918+-firmware.cpio.gz" {
		t.Fatalf("asset names %q %q", name, fwName)
	}
	drivers, firmware := testPacks(t)
	f := newFakeRelease(t, "v1", map[string][]byte{name: gz(t, drivers), fwName: gz(t, firmware)}, "")
	cache := t.TempDir()
	ctx := context.Background()

	pack, info, err := FetchModulePackFrom(ctx, f.srv.Client(), f.srv.URL, cache, "o/r", "apollolake", "DS918+")
	if err != nil {
		t.Fatal(err)
	}
	// WHENCE came along but is not counted as firmware.
	// WHENCE 도 같이 오지만 펌웨어로 세지는 않는다.
	if info.Tag != "v1" || info.FromCache || info.FirmwareErr != nil || info.Firmware != 1 {
		t.Fatalf("first fetch: %+v", info)
	}
	for _, want := range []string{"r8169.ko", kmod.OriginalsList, FirmwarePrefix + "rtl_nic/rtl8168h-2.fw", FirmwarePrefix + "WHENCE"} {
		if _, ok := pack[want]; !ok {
			t.Errorf("merged pack has no %s", want)
		}
	}
	if pack.Modules() != 1 {
		t.Errorf("Modules() = %d, want 1", pack.Modules())
	}

	// The same release again comes from the cache, both assets.
	// 같은 릴리스는 두 번째부터 캐시에서 온다. 두 자산 모두.
	if _, info, err = FetchModulePackFrom(ctx, f.srv.Client(), f.srv.URL, cache, "o/r", "apollolake", "DS918+"); err != nil || !info.FromCache || info.Firmware != 1 {
		t.Fatalf("second fetch: %+v %v", info, err)
	}
	if n := f.downloads.Load(); n != 2 {
		t.Errorf("assets downloaded %d times, want 2", n)
	}

	// GitHub out of reach: the kept copies are used, with their tag.
	// GitHub 에 닿지 못함: 남겨 둔 사본을 태그와 함께 쓴다.
	f.srv.Close()
	again, info, err := FetchModulePackFrom(ctx, http.DefaultClient, f.srv.URL, cache, "o/r", "apollolake", "DS918+")
	if err != nil || !info.FromCache || info.Tag != "v1" || info.Firmware != 1 || !samePack(again, pack) {
		t.Fatalf("offline fetch: %+v %v", info, err)
	}
}

func TestFetchModulePackWithoutFirmware(t *testing.T) {
	name := catalog.ModulePackName("broadwellnk", "DS3622xs+")
	drivers, _ := testPacks(t)
	f := newFakeRelease(t, "v1", map[string][]byte{name: gz(t, drivers)}, "")
	pack, info, err := FetchModulePackFrom(context.Background(), f.srv.Client(), f.srv.URL, "", "o/r", "broadwellnk", "DS3622xs+")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ModulePackFromCPIO(drivers)
	if info.FirmwareErr == nil || info.Firmware != 0 || !samePack(pack, want) {
		t.Fatalf("no firmware asset: %+v", info)
	}
}

func TestFetchModulePackBadFirmwareHash(t *testing.T) {
	name, fwName := catalog.ModulePackName("epyc7002", "SA6400"), catalog.ModuleFirmwareName("epyc7002", "SA6400")
	drivers, firmware := testPacks(t)
	f := newFakeRelease(t, "v1", map[string][]byte{name: gz(t, drivers), fwName: gz(t, firmware)}, fwName)
	cache := t.TempDir()
	pack, info, err := FetchModulePackFrom(context.Background(), f.srv.Client(), f.srv.URL, cache, "o/r", "epyc7002", "SA6400")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ModulePackFromCPIO(drivers)
	if info.FirmwareErr == nil || !strings.Contains(info.FirmwareErr.Error(), "SHA-256") || !samePack(pack, want) {
		t.Fatalf("bad firmware hash: %+v", info)
	}
	if m, _ := filepath.Glob(filepath.Join(cache, "*firmware*")); len(m) != 0 {
		t.Errorf("firmware that failed its hash was cached: %v", m)
	}
}

func TestFetchModulePackRejectsABadHash(t *testing.T) {
	name := catalog.ModulePackName("epyc7002", "SA6400")
	drivers, _ := testPacks(t)
	f := newFakeRelease(t, "v1", map[string][]byte{name: gz(t, drivers)}, name)
	cache := t.TempDir()
	_, _, err := FetchModulePackFrom(context.Background(), f.srv.Client(), f.srv.URL, cache, "o/r", "epyc7002", "SA6400")
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(cache, "modpack-*")); len(m) != 0 {
		t.Errorf("a pack that failed its hash was cached: %v", m)
	}
}

func TestFetchModulePackMissingAsset(t *testing.T) {
	drivers, _ := testPacks(t)
	f := newFakeRelease(t, "v1", map[string][]byte{"broadwellnk-DS3622xs+.cpio.gz": gz(t, drivers)}, "")
	_, _, err := FetchModulePackFrom(context.Background(), f.srv.Client(), f.srv.URL, "", "o/r", "apollolake", "DS918+")
	if err == nil || !strings.Contains(err.Error(), "apollolake-DS918+.cpio.gz") {
		t.Fatalf("err = %v", err)
	}
}

func TestSumFor(t *testing.T) {
	sums := []byte("AB12  a.cpio.gz\ncd34 *b.cpio.gz\n")
	if sumFor(sums, "a.cpio.gz") != "ab12" || sumFor(sums, "b.cpio.gz") != "cd34" || sumFor(sums, "c") != "" {
		t.Fatal("sumFor")
	}
}
