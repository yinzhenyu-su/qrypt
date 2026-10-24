package quark

import "testing"

func TestParseShareLink(t *testing.T) {
	driver := New("cookie", Options{})

	cases := []struct {
		name     string
		raw      string
		wantOK   bool
		wantID   string
		wantPass string
	}{
		{name: "https link", raw: "https://pan.quark.cn/s/19a64ef19d03", wantOK: true, wantID: "19a64ef19d03"},
		{name: "link with passcode", raw: "https://pan.quark.cn/s/19a64ef19d03?pwd=9k2f", wantOK: true, wantID: "19a64ef19d03", wantPass: "9k2f"},
		{name: "http link", raw: "http://pan.quark.cn/s/19a64ef19d03", wantOK: true, wantID: "19a64ef19d03"},
		{name: "www host", raw: "https://www.pan.quark.cn/s/19a64ef19d03", wantOK: true, wantID: "19a64ef19d03"},
		{name: "trailing slash", raw: "https://pan.quark.cn/s/19a64ef19d03/", wantOK: true, wantID: "19a64ef19d03"},
		{name: "bare link id", raw: "19a64ef19d03", wantOK: true, wantID: "19a64ef19d03"},
		{name: "surrounding spaces", raw: "  https://pan.quark.cn/s/19a64ef19d03  ", wantOK: true, wantID: "19a64ef19d03"},
		{name: "foreign provider", raw: "https://pan.baidu.com/s/19a64ef19d03", wantOK: false},
		{name: "unknown host", raw: "https://example.com/s/19a64ef19d03", wantOK: false},
		{name: "missing id", raw: "https://pan.quark.cn/s/", wantOK: false},
		{name: "short id", raw: "https://pan.quark.cn/s/abc", wantOK: false},
		{name: "extra path segment", raw: "https://pan.quark.cn/s/19a64ef19d03/sub", wantOK: false},
		{name: "magnet", raw: "magnet:?xt=urn:btih:064e33ccf69839267b2f4459832ea6c61a1f4807", wantOK: false},
		{name: "empty", raw: "   ", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			link, ok := driver.ParseShareLink(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ParseShareLink(%q) ok = %v, want %v", tc.raw, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if link.LinkID != tc.wantID {
				t.Fatalf("LinkID = %q, want %q", link.LinkID, tc.wantID)
			}
			if link.Passcode != tc.wantPass {
				t.Fatalf("Passcode = %q, want %q", link.Passcode, tc.wantPass)
			}
			if link.URL == "" {
				t.Fatal("URL is empty")
			}
		})
	}
}
