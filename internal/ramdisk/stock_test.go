package ramdisk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibeldr/internal/dsmconf"
)

// stockArchive builds a ramdisk with the three stock files as the DSM 7.4.1
// ramdisks have them, etc.defaults being a symlink to etc.
//
// stockArchive - DSM 7.4.1 램디스크처럼 세 원본 파일을 가진 램디스크를
// 만든다. etc.defaults 는 etc 로의 심볼릭 링크다.
func stockArchive() *Archive {
	a := &Archive{index: map[string]int{}}
	a.Entries = []Entry{
		{Name: "etc.defaults", Mode: ModeSymlink | 0o777, Data: []byte("etc")},
		{Name: synoinfoConf, Mode: ModeRegular | 0o644, Data: []byte("unique=\"synology_broadwellnk_3622xs+\"\nnetif_seq=\"2 3 0 1\"\nsupport_bde_internal_10g=\"yes\"\nrss_server_ssl=\"https://update7.synology.com/autoupdate/genRSS.php\"\n")},
		{Name: versionFile, Mode: ModeRegular | 0o755, Data: []byte("majorversion=\"7\"\nminorversion=\"4\"\nproductversion=\"7.4.1\"\nbuildnumber=\"90080\"\n")},
		{Name: getStateCGI, Mode: ModeRegular | 0o755, Data: []byte("#!/bin/sh\n\nDisabledPortDisks=\"$(/usr/syno/bin/synodiskport -portthawlist)\"\necho \"$DisabledPortDisks\"\n")},
		{Name: linuxrcName, Mode: ModeRegular | 0o755, Data: []byte("# check if broadcom nic need update\nif [ -f \"$Mnt\"/usr/syno/sbin/broadcom_update.sh ]; then\n\t\"$Mnt\"/usr/syno/sbin/broadcom_update.sh \"$Mnt\" update\nfi\n")},
		{Name: ifcfgDir + "/ifcfg-eth0", Mode: ModeRegular | 0o644, Data: []byte("DEVICE=eth0\nBOOTPROTO=static\n")},
		{Name: ifcfgDir + "/ifcfg-eth1", Mode: ModeRegular | 0o644, Data: []byte("DEVICE=eth1\nBOOTPROTO=dhcp\nONBOOT=yes\nIPV6INIT=off\n")},
	}
	for i, e := range a.Entries {
		a.index[e.Name] = i
	}
	return a
}

func entryText(t *testing.T, a *Archive, name string) string {
	t.Helper()
	e, ok := a.Get(name)
	if !ok {
		t.Fatalf("%s missing", name)
	}
	return string(e.Data)
}

// All three edits land, and the carried settings replace the stock values in
// place rather than being appended after them.
//
// 세 수정이 모두 들어가고, 실어 온 설정은 원래 값 뒤에 덧붙지 않고 그 자리에서
// 원래 값을 바꾼다.
func TestEditStock(t *testing.T) {
	a := stockArchive()
	done, missing := a.editStock([]byte("netif_seq=\nrss_server=http://127.0.0.1/x\nsupport_bde_internal_10g=no\n"), nil)
	if len(missing) != 0 {
		t.Fatalf("missing %v", missing)
	}
	if strings.Join(done, ",") != "synoinfo.conf,disabled port disks,broadcom_update.sh,ifcfg eth2 eth3 eth4 eth5 eth6 eth7" {
		t.Fatalf("done %v", done)
	}
	conf := entryText(t, a, synoinfoConf)
	for _, want := range []string{`netif_seq=""`, `support_bde_internal_10g="no"`, `unique="synology_broadwellnk_3622xs+"`} {
		if !strings.Contains(conf, want) {
			t.Errorf("synoinfo.conf lacks %s:\n%s", want, conf)
		}
	}
	// The update server stays the stock one, or the installer loses its
	// online install.
	// 업데이트 서버는 원래 값으로 남는다. 아니면 설치기가 온라인 설치를 잃는다.
	if strings.Contains(conf, "127.0.0.1") {
		t.Errorf("rss_server written into the ramdisk:\n%s", conf)
	}
	if strings.Contains(conf, "2 3 0 1") {
		t.Errorf("stock netif_seq kept:\n%s", conf)
	}
	if got := entryText(t, a, getStateCGI); !strings.Contains(got, "DisabledPortDisks=\"\"\n") || strings.Contains(got, "portthawlist") {
		t.Errorf("get_state.cgi not edited:\n%s", got)
	}
	rc := entryText(t, a, linuxrcName)
	if strings.Contains(rc, "if [ -f \"$Mnt\"/usr/syno/sbin/broadcom_update.sh ]") || !strings.Contains(rc, "broadcom_update.sh.vibeldr-skip") {
		t.Errorf("linuxrc not edited:\n%s", rc)
	}
	// Existing interface files are kept as they are; the new ones are DHCP.
	// 이미 있는 인터페이스 파일은 그대로 두고, 새 파일은 DHCP 다.
	if got := entryText(t, a, ifcfgDir+"/ifcfg-eth0"); !strings.Contains(got, "static") {
		t.Errorf("ifcfg-eth0 replaced: %s", got)
	}
	if got := entryText(t, a, ifcfgDir+"/ifcfg-eth7"); got != "DEVICE=eth7\nBOOTPROTO=dhcp\nONBOOT=yes\nIPV6INIT=off\n" {
		t.Errorf("ifcfg-eth7: %q", got)
	}
	// The symlink is not turned into a file.
	// 심볼릭 링크가 파일로 바뀌지 않는다.
	if l, _ := a.Get("etc.defaults"); l.IsRegular() || string(l.Data) != "etc" {
		t.Errorf("etc.defaults changed: %+v", l)
	}
}

// With no carried settings synoinfo.conf is left alone, and a line DSM does
// not have is reported instead of failing.
//
// 실어 온 설정이 없으면 synoinfo.conf 는 그대로 두고, DSM 에 없는 줄은 실패
// 대신 보고한다.
func TestEditStockMissingLines(t *testing.T) {
	a := stockArchive()
	e, _ := a.Get(getStateCGI)
	e.Data = []byte("#!/bin/sh\nDisabledPortDisks=\"\"\n")
	before := entryText(t, a, synoinfoConf)
	done, missing := a.editStock(nil, nil)
	if strings.Join(done, ",") != "broadcom_update.sh,ifcfg eth2 eth3 eth4 eth5 eth6 eth7" || strings.Join(missing, ",") != "disabled port disks" {
		t.Fatalf("done %v, missing %v", done, missing)
	}
	if entryText(t, a, synoinfoConf) != before {
		t.Error("synoinfo.conf changed without settings")
	}
}

// With the build's release, the ramdisk gets a list naming its own model and
// that release, and rss_server_ssl points at the helper; without its MD5
// nothing changes.
//
// 빌드의 릴리스가 있으면 램디스크는 자기 모델과 그 릴리스를 적은 목록을 받고,
// rss_server_ssl 은 헬퍼를 가리킨다. MD5 가 없으면 아무것도 바뀌지 않는다.
func TestEditStockInstallRSS(t *testing.T) {
	a := stockArchive()
	url := "https://global.synologydownload.com/download/DSM/release/7.4.1/90080/DSM_DS3622xs+_90080.pat"
	done, missing := a.editStock(nil, dsmconf.RenderRelease(url, "de1634a7ff005befe77a62c294e541d6"))
	if len(missing) != 0 || !strings.Contains(strings.Join(done, ","), "install RSS") {
		t.Fatalf("done %v, missing %v", done, missing)
	}
	list := entryText(t, a, dsmconf.InstallRSSName)
	for _, want := range []string{"<title>DSM 7.4.1-90080</title>", "<mUnique>synology_broadwellnk_3622xs+</mUnique>", "<mLink>" + url + "</mLink>"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %s:\n%s", want, list)
		}
	}
	if conf := entryText(t, a, synoinfoConf); !strings.Contains(conf, `rss_server_ssl="`+dsmconf.InstallRSSURL+`"`) {
		t.Errorf("rss_server_ssl not pointed at the helper:\n%s", conf)
	}

	b := stockArchive()
	b.editStock(nil, dsmconf.RenderRelease(url, ""))
	if _, ok := b.Get(dsmconf.InstallRSSName); ok || strings.Contains(entryText(t, b, synoinfoConf), "127.0.0.1") {
		t.Error("a release without its MD5 changed the ramdisk")
	}
}

// The lines exist in the real DSM 7.4.1 ramdisks of all three models, so no
// edit is left out there. Skipped when work/dsmcore has not been fetched.
//
// 세 모델의 실제 DSM 7.4.1 램디스크에 그 줄들이 있어서, 거기서는 빠지는 수정이
// 없다. work/dsmcore 를 받아 두지 않았으면 건너뛴다.
func TestEditStockRealRamdisks(t *testing.T) {
	for _, model := range []string{"DS918+", "DS3622xs+", "SA6400"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "work", "dsmcore", model, "rd.gz"))
		if err != nil {
			t.Skipf("no rd.gz for %s", model)
		}
		release := File{Name: dsmconf.ReleaseName, Mode: 0o644, Data: dsmconf.RenderRelease("https://example/"+model+".pat", "0123")}
		out, rep, err := Patch(raw, []byte("helper"), File{Name: SynoinfoName, Mode: 0o644, Data: []byte("netif_seq=\n")}, release)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		// DS918+ also gets ifcfg-eth2..7; the other two already have them.
		// DS918+ 는 ifcfg-eth2..7 도 받는다. 나머지 둘은 이미 갖고 있다.
		want := 4
		if model == "DS918+" {
			want = 5
		}
		if len(rep.StockMissing) != 0 || len(rep.Stock) != want {
			t.Errorf("%s: edited %v, not found %v", model, rep.Stock, rep.StockMissing)
		}
		a, err := ReadCPIO(out)
		if err != nil {
			t.Fatal(err)
		}
		if list := entryText(t, a, dsmconf.InstallRSSName); !strings.Contains(list, "<title>DSM 7.4.1-90080</title>") {
			t.Errorf("%s: list\n%s", model, list)
		}
	}
}
