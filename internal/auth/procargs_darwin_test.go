package auth

import (
	"encoding/binary"
	"os"
	"slices"
	"strings"
	"testing"
)

// procargs2 builds a kern.procargs2 buffer the way the macOS kernel lays it
// out: argc, the executable path padded with NULs to a multiple of 8 bytes,
// then the arguments and the environment, each NUL-terminated.
func procargs2(exe string, pad int, argv, env []string) []byte {
	buf := binary.LittleEndian.AppendUint32(nil, uint32(len(argv)))
	buf = append(buf, exe...)
	buf = append(buf, strings.Repeat("\x00", pad)...)
	for _, s := range append(slices.Clone(argv), env...) {
		buf = append(buf, s...)
		buf = append(buf, 0)
	}
	return buf
}

func TestParseProcArgs(t *testing.T) {
	const exe = "/Applications/B.app/Contents/MacOS/B" // 36 bytes, so 4 NULs of padding
	marker := "--user-data-dir=/tmp/deplexity-login-1"
	withArgc := func(buf []byte, argc uint32) []byte {
		binary.LittleEndian.PutUint32(buf, argc)
		return buf
	}
	for _, tc := range []struct {
		name string
		buf  []byte
		want []string
	}{
		{"plain", procargs2(exe, 4, []string{"B", marker}, []string{"HOME=/"}), []string{"B", marker}},
		{"aligned path gets a full block", procargs2("/bin/abc", 8, []string{"abc"}, nil), []string{"abc"}},
		{"empty argv0", procargs2(exe, 4, []string{"", "B"}, []string{marker}), nil},
		{"only empty arguments", procargs2(exe, 4, []string{"", ""}, []string{marker}), nil},
		{"less padding than alignment", procargs2(exe, 3, []string{"B", marker}, nil), nil},
		{"more padding than alignment", procargs2(exe, 6, []string{"B"}, nil), nil},
		{"fewer arguments than argc", withArgc(procargs2(exe, 4, []string{"B", marker}, nil), 3), nil},
		{"unterminated last argument", func() []byte { b := procargs2(exe, 4, []string{"B", marker}, nil); return b[:len(b)-1] }(), nil},
		{"zero argc", withArgc(procargs2(exe, 4, nil, []string{marker}), 0), nil},
		{"many empty arguments", procargs2(exe, 4, append(make([]string, 5000), "B"), nil), nil},
		{"too short", []byte{1, 0}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := parseProcArgs(tc.buf)
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseProcArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

// A kernel padding one NUL less than expected, combined with an empty
// argv[0], produces exactly the bytes of a normal buffer, so no parser can
// tell them apart. That is why killProfileProcesses first checks the layout
// on this process (procArgsLayoutOK) and kills nothing when it differs.
func TestProcArgsLayoutMatchesThisSystem(t *testing.T) {
	if !procArgsLayoutOK() {
		t.Fatalf("kern.procargs2 of this process parsed as %q, want %q", procArgs(os.Getpid()), os.Args)
	}
}
