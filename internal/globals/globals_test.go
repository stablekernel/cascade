package globals

import "testing"

func TestDryRun(t *testing.T) {
	// Reset to default
	SetDryRun(false)

	if DryRun() {
		t.Error("expected DryRun() to be false initially")
	}

	SetDryRun(true)
	if !DryRun() {
		t.Error("expected DryRun() to be true after SetDryRun(true)")
	}

	SetDryRun(false)
	if DryRun() {
		t.Error("expected DryRun() to be false after SetDryRun(false)")
	}
}

func TestJSON(t *testing.T) {
	// Reset to default
	SetJSON(false)

	if JSON() {
		t.Error("expected JSON() to be false initially")
	}

	SetJSON(true)
	if !JSON() {
		t.Error("expected JSON() to be true after SetJSON(true)")
	}

	SetJSON(false)
	if JSON() {
		t.Error("expected JSON() to be false after SetJSON(false)")
	}
}

func TestConcurrentAccess(t *testing.T) {
	// Test that concurrent access doesn't panic
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				SetDryRun(true)
				_ = DryRun()
				SetDryRun(false)
				SetJSON(true)
				_ = JSON()
				SetJSON(false)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
