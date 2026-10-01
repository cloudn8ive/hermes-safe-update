package platform

import (
	"reflect"
	"testing"
)

func TestParseKey(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want Key
	}{
		{"lower y", []byte("y"), Key{Rune: 'y'}},
		{"upper Y is lower-cased", []byte("Y"), Key{Rune: 'y'}},
		{"enter CR", []byte{'\r'}, Key{Rune: '\r'}},
		{"enter LF", []byte{'\n'}, Key{Rune: '\r'}},
		{"lone esc", []byte{0x1b}, Key{Rune: 0x1b}},
		{"ctrl-c", []byte{0x03}, Key{CtrlC: true}},
		{"arrow up", []byte("\x1b[A"), Key{Other: true}},
		{"function key", []byte("\x1bOP"), Key{Other: true}},
		{"alt-x is not a bare esc", []byte("\x1bx"), Key{Other: true}},
		{"unicode", []byte("é"), Key{Rune: 'é'}},
		{"other control byte", []byte{0x01}, Key{Other: true}},
		{"empty", nil, Key{Other: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseKey(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseKey(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestOSCProgress(t *testing.T) {
	cases := []struct {
		state ProgressState
		frac  float64
		want  string
	}{
		{ProgressNone, 0, "\x1b]9;4;0;0\a"},
		{ProgressNormal, 0.426, "\x1b]9;4;1;43\a"},
		{ProgressNormal, 1.7, "\x1b]9;4;1;100\a"},
		{ProgressNormal, -1, "\x1b]9;4;1;0\a"},
		{ProgressError, 0.5, "\x1b]9;4;2;50\a"},
		{ProgressIndeterminate, 0, "\x1b]9;4;3;0\a"},
		{ProgressPaused, 0.2, "\x1b]9;4;4;20\a"},
	}
	for _, c := range cases {
		if got := oscProgress(c.state, c.frac); got != c.want {
			t.Errorf("oscProgress(%v, %v) = %q, want %q", c.state, c.frac, got, c.want)
		}
	}
}

func TestParseMounts(t *testing.T) {
	in := "/dev/nvme0n1p2 / ext4 rw 0 0\n" +
		"proc /proc proc rw 0 0\n" +
		"/dev/loop3 /snap/core/1 squashfs ro 0 0\n" +
		"/dev/sdb1 /mnt/data\\040disk ext4 rw 0 0\n" +
		"tmpfs /run tmpfs rw 0 0\n" +
		"//nas/share /mnt/nas cifs rw 0 0\n" +
		"/dev/sdb1 /mnt/data\\040disk ext4 rw 0 0\n" +
		"short\n"
	got := parseMounts(in)
	want := []string{"/", "/mnt/data disk"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
