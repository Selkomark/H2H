package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
)

// covering is h with trust that holds for the folders inside a trusted one.
func covering(h config.Harness) config.Harness {
	h.TrustCoversSubfolders = true
	return h
}

// Only a harness documented to cover them takes a folder above as trust.
func TestOnlyCopilotTakesTrustFromAFolderAbove(t *testing.T) {
	for name, h := range config.Shipped().Harnesses {
		if h.TrustCoversSubfolders != (name == "copilot") {
			t.Errorf("%s: trust_covers_subfolders = %t", name, h.TrustCoversSubfolders)
		}
	}
}

func TestTrusted(t *testing.T) {
	const project = "/work/team/app"
	keyed := config.Harness{TrustFile: "~/h.json", TrustKey: []string{"projects", "{project}", "accepted"}, TrustValue: "true"}
	listed := config.Harness{TrustFile: "~/h.json", TrustKey: []string{"trustedFolders"}, TrustValue: "{project}"}
	inTOML := config.Harness{TrustFile: "~/h.toml", TrustKey: []string{"projects", "{project}", "trust_level"}, TrustValue: "trusted"}
	tests := []struct {
		name    string
		h       config.Harness
		file    string // "" for no file
		want    bool
		wantErr bool
	}{
		{"keyed, trusted", keyed, `{"projects": {"/work/team/app": {"accepted": true}}}`, true, false},
		{"keyed, refused", keyed, `{"projects": {"/work/team/app": {"accepted": false}}}`, false, false},
		{"keyed, another project", keyed, `{"projects": {"/work/other": {"accepted": true}}}`, false, false},
		{"keyed, a folder above", keyed, `{"projects": {"/work/team": {"accepted": true}}}`, false, false},
		{"keyed, a folder above that covers", covering(keyed), `{"projects": {"/work/team": {"accepted": true}}}`, true, false},
		{"keyed, a sibling with the same prefix", keyed, `{"projects": {"/work/team/app2": {"accepted": true}}}`, false, false},
		{"listed, trusted", listed, `{"trustedFolders": ["/x", "/work/team/app"]}`, true, false},
		{"listed, a folder above", listed, `{"trustedFolders": ["/work"]}`, false, false},
		{"listed, a folder above that covers", covering(listed), `{"trustedFolders": ["/work"]}`, true, false},
		{"listed, not there", listed, `{"trustedFolders": ["/x"]}`, false, false},
		{"listed, no list yet", listed, "// managed automatically\n{\"firstLaunchAt\": \"today\"}\n", false, false},
		{"toml, trusted", inTOML, "[projects.\"/work/team/app\"]\ntrust_level = \"trusted\"\n", true, false},
		{"toml, untrusted", inTOML, "[projects.\"/work/team/app\"]\ntrust_level = \"untrusted\"\n", false, false},
		{"no file", keyed, "", false, false},
		{"unreadable file", keyed, "{not json", false, true},
		{"no trust settings", config.Harness{}, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.file != "" {
				if err := os.WriteFile(TrustPath(tt.h, home), []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Trusted(tt.h, project, home)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("Trusted() = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestTrustPath(t *testing.T) {
	if got := TrustPath(config.Harness{TrustFile: "~/.x/c.json"}, "/home/u"); got != filepath.Join("/home/u", ".x", "c.json") {
		t.Errorf("TrustPath with ~ = %q", got)
	}
	if got := TrustPath(config.Harness{TrustFile: "/etc/c.json"}, "/home/u"); got != "/etc/c.json" {
		t.Errorf("TrustPath of an absolute path = %q", got)
	}
}
