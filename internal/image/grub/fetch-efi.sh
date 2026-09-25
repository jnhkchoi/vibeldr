#!/usr/bin/env bash
# fetch-efi.sh - Debian sid 의 grub-efi-amd64-signed 패키지에서 monolithic
# grubx64.efi 를 뽑아 이 디렉터리의 BOOTX64.EFI 로 저장한다.
#
# go generate 로 실행되며 (grub.go 의 //go:generate 지시자 참고), 필요한
# 도구는 curl / python3 / xz / tar 뿐이다. `ar` 는 없어도 됨 - Python 이
# ar 아카이브 (.deb 컨테이너) 를 직접 읽는다.
#
# 실패 시 (네트워크 오프라인 등) BOOTX64.EFI 는 그대로 두고 종료 상태
# 1 을 반환한다. 그 상태에서 Builder.WithEFI = true 로 빌드하면
# validateEFIBinary 가 명시적 안내와 함께 실패한다.
set -euo pipefail

DEB_URL="https://ftp.debian.org/debian/pool/main/g/grub-efi-amd64-signed/grub-efi-amd64-signed_1+2.14+3_amd64.deb"
INNER="./usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed"

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "fetch-efi: downloading $DEB_URL" >&2
curl -fsSL -o "$TMP/pkg.deb" "$DEB_URL"

python3 - "$TMP/pkg.deb" "$TMP" << 'PY'
import sys, os
deb, out = sys.argv[1], sys.argv[2]
with open(deb, "rb") as f:
    data = f.read()
assert data[:8] == b"!<arch>\n", "not a .deb (ar archive)"
off = 8
while off < len(data):
    hdr = data[off:off+60]
    if len(hdr) < 60: break
    name = hdr[0:16].decode().rstrip()
    size = int(hdr[48:58].decode().rstrip())
    off += 60
    if name.startswith("data.tar"):
        with open(os.path.join(out, name), "wb") as g:
            g.write(data[off:off+size])
    off += size
    if size % 2: off += 1
PY

if [ -f "$TMP/data.tar.xz" ]; then
    xz -d < "$TMP/data.tar.xz" > "$TMP/data.tar"
elif [ -f "$TMP/data.tar.zst" ]; then
    zstd -d < "$TMP/data.tar.zst" > "$TMP/data.tar"
else
    echo "fetch-efi: no data.tar.* found in .deb" >&2
    exit 1
fi

tar -xf "$TMP/data.tar" -C "$TMP" "$INNER"
cp "$TMP/$INNER" "$HERE/BOOTX64.EFI"
echo "fetch-efi: wrote $HERE/BOOTX64.EFI ($(wc -c < "$HERE/BOOTX64.EFI") bytes)" >&2
