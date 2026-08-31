package hikvision

import (
	"strings"
	"testing"
)

func TestDigestHeader(t *testing.T) {
	h, e := digestHeader(`Digest realm="NVR", nonce="abc123", qop="auth", algorithm=MD5`, "operator", "secret", "GET", "/ISAPI/System/deviceInfo")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{`username="operator"`, `realm="NVR"`, `uri="/ISAPI/System/deviceInfo"`, `qop=auth`, `nc=00000001`} {
		if !strings.Contains(h, want) {
			t.Fatalf("header %q missing %q", h, want)
		}
	}
}
func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`rtsp://host/a?x=1&y=<two>`); got != `rtsp://host/a?x=1&amp;y=&lt;two&gt;` {
		t.Fatalf("unexpected XML escape: %s", got)
	}
}
