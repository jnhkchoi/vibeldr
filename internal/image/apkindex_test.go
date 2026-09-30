package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// apkindexServer serves an APKINDEX.tar.gz holding index as its APKINDEX file.
// apkindexServer 는 index 를 APKINDEX 파일로 담은 APKINDEX.tar.gz 를 내준다.
func apkindexServer(t *testing.T, index string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "APKINDEX", Mode: 0o644, Size: int64(len(index))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(index)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/APKINDEX.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const testAPKINDEX = "P:linux-lts\nV:6.6.60-r0\n\nP:ca-certificates\nV:20240705-r0\n\nP:linux-lts\nV:6.6.61-r0\n\n"

// TestLatestPackageVersion picks the newest V: of the package asked for, not
// of any other package in the index.
//
// TestLatestPackageVersion 은 색인의 다른 패키지가 아니라, 요청한 패키지의
// 가장 새 V: 를 고른다.
func TestLatestPackageVersion(t *testing.T) {
	srv := apkindexServer(t, testAPKINDEX)
	for pkg, want := range map[string]string{"linux-lts": "6.6.61-r0", "ca-certificates": "20240705-r0"} {
		got, err := latestPackageVersion(context.Background(), srv.Client(), srv.URL, pkg)
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", pkg, got, want)
		}
	}
}

// TestLatestPackageVersionMissingNamesPackage checks that the error for a
// package missing from the index names that package.
//
// TestLatestPackageVersionMissingNamesPackage 는 색인에 없는 패키지의 오류가
// 그 패키지 이름을 담는지 본다.
func TestLatestPackageVersionMissingNamesPackage(t *testing.T) {
	srv := apkindexServer(t, testAPKINDEX)
	_, err := latestPackageVersion(context.Background(), srv.Client(), srv.URL, "no-such-pkg")
	if err == nil {
		t.Fatal("no error for a missing package")
	}
	if !strings.Contains(err.Error(), "no-such-pkg") {
		t.Errorf("error does not name the package: %v", err)
	}
}
