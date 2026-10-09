package transport

import "testing"

func TestSubflowInfoVersioning(t *testing.T) {
	if _, known := decodeSubflows(make([]byte, 80)); known {
		t.Fatal("old kernel reported a fabricated count")
	}
	info := make([]byte, 96)
	info[80] = 3
	if count, known := decodeSubflows(info); !known || count != 3 {
		t.Fatal("incorrect subflow count", count, known)
	}
	if _, known := subflows(nil); known {
		t.Fatal("missing socket reported a count")
	}
}
