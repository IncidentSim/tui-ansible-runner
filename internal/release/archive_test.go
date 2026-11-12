package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivesContainOnlyUserDistributionAndExecutableMode(t *testing.T) {
	for _, windows := range []bool{false, true} {
		t.Run(fmt.Sprint(windows), func(t *testing.T) {
			dir := t.TempDir()
			binary := "incidentctl"
			if windows {
				binary += ".exe"
			}
			contents := map[string]string{binary: "binary", "README.md": "USER GUIDE", "incidentctl.example.json": `{"enabled":false}`}
			files := []archiveFile{}
			for _, name := range []string{binary, "README.md", "incidentctl.example.json"} {
				source := filepath.Join(dir, name)
				if err := os.WriteFile(source, []byte(contents[name]), 0600); err != nil {
					t.Fatal(err)
				}
				mode := int64(0644)
				if name == binary {
					mode = 0755
				}
				files = append(files, archiveFile{name, source, mode})
			}
			// A local user configuration must never be picked up from the folder.
			os.WriteFile(filepath.Join(dir, "incidentctl.json"), []byte("PRIVATE-CONFIG"), 0600)
			archive := filepath.Join(dir, "distribution")
			if err := writeArchive(archive, files, windows); err != nil {
				t.Fatal(err)
			}
			seen := map[string]string{}
			if windows {
				reader, err := zip.OpenReader(archive)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				for _, file := range reader.File {
					entry, err := file.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(entry)
					entry.Close()
					if err != nil {
						t.Fatal(err)
					}
					seen[file.Name] = string(data)
				}
			} else {
				file, err := os.Open(archive)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				compressed, err := gzip.NewReader(file)
				if err != nil {
					t.Fatal(err)
				}
				defer compressed.Close()
				reader := tar.NewReader(compressed)
				for {
					header, err := reader.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if header.Name == binary && header.Mode != 0755 {
						t.Fatalf("binary lost executable mode: %o", header.Mode)
					}
					data, err := io.ReadAll(reader)
					if err != nil {
						t.Fatal(err)
					}
					seen[header.Name] = string(data)
				}
			}
			if len(seen) != len(contents) {
				t.Fatalf("unexpected archive contents: %v", seen)
			}
			for name, content := range contents {
				if seen[name] != content {
					t.Fatalf("wrong distribution file %s", name)
				}
			}
			if err := checksums(dir, []string{"distribution"}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(archive)
			checksum, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
			if string(checksum) != fmt.Sprintf("%x  distribution\n", sha256.Sum256(data)) {
				t.Fatal("incorrect distribution checksum")
			}
		})
	}
}

func TestSourceCopyExcludesGeneratedBinariesAndPrivateConfig(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example", "go.sum": "",
		"cmd/incidentctl/main.go": "package main", "cmd/incidentctl/prompts/example.md": "PROMPT",
		"internal/auditbinary/binary.go":               "package auditbinary",
		"internal/auditbinary/binaries/other-platform": "STALE-BINARY",
		"incidentctl.json":                             "PRIVATE-CONFIG", "bin/incidentctl": "OLD-TUI",
	} {
		name = filepath.Join(source, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(name), 0755)
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copySources(source, destination); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"incidentctl.json", "bin", "internal/auditbinary/binaries"} {
		if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("build staging copied %s", name)
		}
	}
	if data, err := os.ReadFile(filepath.Join(destination, "cmd/incidentctl/prompts/example.md")); err != nil || string(data) != "PROMPT" {
		t.Fatal("build staging lost embedded prompt", err)
	}
}

func TestReleaseVersionAndTargetsValidation(t *testing.T) {
	for _, version := range []string{"v0.1.0", "v1.2.3-rc.1"} {
		if !releaseVersion.MatchString(version) {
			t.Fatal(version)
		}
	}
	for _, version := range []string{"", "../v1.0.0", "v1.0", "v1.0.0;echo", "v01.0.0"} {
		if releaseVersion.MatchString(version) {
			t.Fatalf("unsafe version accepted: %s", version)
		}
	}
	if targets, err := parseTargets(DefaultTargets); err != nil || len(targets) != 4 {
		t.Fatal(targets, err)
	}
	for _, targets := range []string{"linux/386", "windows/amd64,windows/amd64", "../linux/amd64", "linux/amd64,"} {
		if _, err := parseTargets(targets); err == nil {
			t.Fatal("invalid targets accepted:", targets)
		}
	}
	if _, err := Build(Options{Version: "../bad"}); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatal(err)
	}
}
