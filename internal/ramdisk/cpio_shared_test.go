package ramdisk

import "testing"

// TestReadCPIOSharedAliases: ReadCPIOShared hands out windows into the buffer
// it was given, while ReadCPIO still copies.
//
// TestReadCPIOSharedAliases - ReadCPIOShared 는 받은 버퍼를 들여다보는 창을 주고,
// ReadCPIO 는 여전히 복사한다.
func TestReadCPIOSharedAliases(t *testing.T) {
	a := NewArchive()
	if err := a.Add("x.ko", ModeRegular|0o644, []byte("module")); err != nil {
		t.Fatal(err)
	}
	blob, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ReadCPIOShared(blob)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := ReadCPIO(blob)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := shared.Get("x.ko")
	c, _ := copied.Get("x.ko")
	s.Data[0] = 'M'
	if indexOf(blob, "Module") < 0 {
		t.Error("ReadCPIOShared copied the body")
	}
	if string(c.Data) != "module" {
		t.Errorf("ReadCPIO shares the body: %q", c.Data)
	}
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}
