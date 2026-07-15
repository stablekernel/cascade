package git

import "testing"

// TestRetryablePushRejection pins the push-failure taxonomy shared with
// internal/statewrite's classifyPutError and the generated state-write shell:
// stale-ref rejections and transient transport failures retry within the
// bounded loop (the rebase is a no-op on a transport blip; the retry itself
// is the fix), while remote-side policy rejections (GH013 workflow scope,
// branch protection pre-receive hooks) and auth failures fail fast with the
// remote's real output.
func TestRetryablePushRejection(t *testing.T) {
	retryable := map[string]string{
		"non-fast-forward":    "! [rejected] main -> main (non-fast-forward)",
		"fetch first":         "! [rejected] main -> main (fetch first)",
		"ref lock":            "error: cannot lock ref 'refs/heads/main': is at abc but expected def",
		"http 5xx via rpc":    "error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502",
		"http 5xx plain":      "fatal: unable to access 'https://github.com/o/r.git/': The requested URL returned error: 500",
		"connection reset":    "fatal: unable to access 'https://github.com/o/r.git/': OpenSSL SSL_read: Connection reset by peer, errno 104",
		"timeout":             "fatal: unable to access 'https://github.com/o/r.git/': Failed to connect to github.com port 443: Connection timed out",
		"dns failure":         "fatal: unable to access 'https://github.com/o/r.git/': Could not resolve host: github.com",
		"early eof":           "fatal: early EOF\nfatal: the remote end hung up unexpectedly",
		"remote hung up":      "fatal: the remote end hung up unexpectedly",
		"sideband disconnect": "send-pack: unexpected disconnect while reading sideband packet",
		"tls handshake":       "fatal: unable to access 'https://github.com/o/r.git/': gnutls_handshake() failed: The TLS connection was non-properly terminated.",
	}
	for name, out := range retryable {
		t.Run("retryable/"+name, func(t *testing.T) {
			if !retryablePushRejection([]byte(out)) {
				t.Errorf("retryablePushRejection(%q) = false, want true: a transient failure must retry", out)
			}
		})
	}

	permanent := map[string]string{
		"gh013 workflow scope": "! [remote rejected] main -> main (refusing to allow a Personal Access Token to create or update workflow '.github/workflows/orchestrate.yaml' without 'workflow' scope)",
		"gh013 marker":         "remote: error: GH013: Repository rule violations found for refs/heads/main.",
		"branch protection":    "! [remote rejected] main -> main (protected branch hook declined)",
		"auth failure":         "fatal: Authentication failed for 'https://github.com/o/r.git/'",
		"bad credentials":      "remote: Invalid username or password.\nfatal: unable to access 'https://github.com/o/r.git/': The requested URL returned error: 403",
		"repo not found":       "remote: Repository not found.\nfatal: repository 'https://github.com/o/r.git/' not found",
	}
	for name, out := range permanent {
		t.Run("permanent/"+name, func(t *testing.T) {
			if retryablePushRejection([]byte(out)) {
				t.Errorf("retryablePushRejection(%q) = true, want false: a policy or auth rejection must fail fast", out)
			}
		})
	}
}
