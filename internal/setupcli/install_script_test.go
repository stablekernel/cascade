package setupcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const archiveName = "cascade_9.9.9_linux_amd64.tar.gz"

// fakeBinary is the payload packed as the "cascade" binary inside the fixture
// archive. Its exact bytes are asserted after a successful install.
const fakeBinary = "#!/bin/sh\necho 'cascade version v9.9.9'\n"

// ghStub is a gh CLI stand-in that serves `gh release download` from a local
// fixture directory instead of the network. Any asset matching -p is copied
// into the -D destination; a pattern with no match exits 1, exactly like gh
// reporting a missing release asset.
const ghStub = `#!/usr/bin/env bash
set -eu
pattern=""
dest="."
while [ "$#" -gt 0 ]; do
  case "$1" in
    -p) pattern="$2"; shift 2 ;;
    -D) dest="$2"; shift 2 ;;
    *) shift ;;
  esac
done
found=0
for f in "$GH_STUB_RELEASE_DIR"/$pattern; do
  [ -e "$f" ] || continue
  cp "$f" "$dest/"
  found=1
done
if [ "$found" -ne 1 ]; then
  echo "release asset not found: $pattern" >&2
  exit 1
fi
`

// cosignRejectStub models a cosign whose verify-blob rejects the signature.
// The --help probe answers without the --new-bundle-format flag so the script
// exercises the plain invocation.
const cosignRejectStub = `#!/usr/bin/env bash
case "$*" in
  *--help*) echo "verify-blob usage"; exit 0 ;;
esac
echo "Error: verifying blob: no matching signatures" >&2
exit 1
`

// cosignAcceptStub models a cosign whose verify-blob accepts the signature.
// The --help probe answers without the --new-bundle-format flag so the script
// exercises the plain invocation, and verify-blob exits 0 so a present cosign
// drives the authenticity path to success rather than the sha256-only fallback.
const cosignAcceptStub = `#!/usr/bin/env bash
case "$*" in
  *--help*) echo "verify-blob usage"; exit 0 ;;
esac
exit 0
`

// installScriptPath locates .github/actions/setup-cli/install.sh from this
// test file's position in the tree.
func installScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	script := filepath.Join(root, ".github", "actions", "setup-cli", "install.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("install script missing: %v", err)
	}
	return script
}

// writeArchive builds the fixture release archive: a gzipped tar carrying the
// fake cascade binary. It returns the archive bytes so callers can hash them.
func writeArchive(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "cascade", Mode: 0o755, Size: int64(len(fakeBinary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(fakeBinary)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, archiveName), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeRelease builds a fixture release directory: the archive plus a
// checksums.txt whose entry for the archive is produced by mutate (identity
// for a good release, a corrupter for a tampered one).
func makeRelease(t *testing.T, mutate func(sum string) string) string {
	t.Helper()
	dir := t.TempDir()
	sum := fmt.Sprintf("%x", sha256.Sum256(writeArchive(t, dir)))
	if mutate != nil {
		sum = mutate(sum)
	}
	entry := fmt.Sprintf("%s  %s\n", sum, archiveName)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// installStub writes script as an executable command named name into binDir.
func installStub(t *testing.T, binDir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runInstall executes install.sh against the fixture release, with gh (and any
// extra stubs) served from a stub bin directory. It returns the combined
// output, the run error, and the path the binary would land at.
func runInstall(t *testing.T, releaseDir string, extraStubs map[string]string) (string, string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is a bash script; not exercised on windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	stubBin := t.TempDir()
	installStub(t, stubBin, "gh", ghStub)
	for name, script := range extraStubs {
		installStub(t, stubBin, name, script)
	}
	installDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", installScriptPath(t))
	cmd.Env = append(os.Environ(),
		"PATH="+stubBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_STUB_RELEASE_DIR="+releaseDir,
		"TAG=v9.9.9",
		"ARCHIVE_PATTERN=cascade_*_linux_amd64.tar.gz",
		"CASCADE_INSTALL_DIR="+installDir,
	)
	out, err := cmd.CombinedOutput()
	return string(out), filepath.Join(installDir, "cascade"), err
}

func TestInstallScript_KnownGoodChecksumInstalls(t *testing.T) {
	release := makeRelease(t, nil)

	out, installed, err := runInstall(t, release, nil)
	if err != nil {
		t.Fatalf("install.sh failed on a good release: %v\n%s", err, out)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("installed binary missing after a verified install: %v\n%s", err, out)
	}
	if string(got) != fakeBinary {
		t.Fatalf("installed binary does not match the archive payload:\n%q", got)
	}
}

func TestInstallScript_TamperedChecksumFailsClosed(t *testing.T) {
	// Flip the leading hash byte: the archive downloads fine but no longer
	// matches its checksums.txt entry, the signature a tampered mirror or a
	// swapped asset would leave.
	release := makeRelease(t, func(sum string) string {
		if sum[0] == '0' {
			return "1" + sum[1:]
		}
		return "0" + sum[1:]
	})

	out, installed, err := runInstall(t, release, nil)
	if err == nil {
		t.Fatalf("install.sh succeeded on a tampered checksum; the integrity gate must fail closed\n%s", out)
	}
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Fatalf("a binary was installed despite the checksum mismatch\n%s", out)
	}
}

func TestInstallScript_MissingChecksumEntryFailsClosed(t *testing.T) {
	// checksums.txt exists but has no entry for the archive: refusing is the
	// only safe answer, since nothing vouches for the downloaded bytes.
	release := t.TempDir()
	writeArchive(t, release)
	if err := os.WriteFile(filepath.Join(release, "checksums.txt"),
		[]byte("0000000000000000000000000000000000000000000000000000000000000000  some_other_asset.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, installed, err := runInstall(t, release, nil)
	if err == nil {
		t.Fatalf("install.sh succeeded with no checksum entry for the archive\n%s", out)
	}
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Fatalf("a binary was installed despite the missing checksum entry\n%s", out)
	}
}

func TestInstallScript_CosignRejectionFailsClosed(t *testing.T) {
	// The release carries a signature bundle and cosign is present but rejects
	// it. A failed verification must abort the install: the warn-and-continue
	// fallback is only for a missing bundle or missing cosign, never for a
	// verification that ran and said no.
	release := makeRelease(t, nil)
	if err := os.WriteFile(filepath.Join(release, "checksums.txt.bundle"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, installed, err := runInstall(t, release, map[string]string{"cosign": cosignRejectStub})
	if err == nil {
		t.Fatalf("install.sh succeeded although cosign rejected the checksum signature\n%s", out)
	}
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Fatalf("a binary was installed despite the rejected signature\n%s", out)
	}
}

func TestInstallScript_CosignAcceptanceVerifiesAndInstalls(t *testing.T) {
	// The release carries a signature bundle and a present cosign accepts it.
	// This is the path the setup-cli action now guarantees on stock runners by
	// installing cosign: verification must actually run (not the sha256-only
	// fallback) and the install must succeed. The "verified" line proves the
	// authenticity gate was exercised rather than skipped with a warning.
	release := makeRelease(t, nil)
	if err := os.WriteFile(filepath.Join(release, "checksums.txt.bundle"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, installed, err := runInstall(t, release, map[string]string{"cosign": cosignAcceptStub})
	if err != nil {
		t.Fatalf("install.sh failed although cosign accepted the signature: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cosign signature on checksums.txt verified.") {
		t.Fatalf("expected the signature-verified path to run, got:\n%s", out)
	}
	got, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("installed binary missing after a verified install: %v\n%s", err, out)
	}
	if string(got) != fakeBinary {
		t.Fatalf("installed binary does not match the archive payload:\n%q", got)
	}
}
