package harness

import (
	"testing"
)

func TestParseActOutput(t *testing.T) {
	// Sample act JSON output (matches actual act format)
	// Act uses jobID as the job identifier and msg patterns for lifecycle events
	jsonOutput := `{"jobID":"build-app","msg":"⭐ Run Set up job","level":"info"}
{"jobID":"build-app","step":"checkout","msg":"Checkout code","level":"info"}
{"jobID":"build-app","step":"checkout","stepResult":"success","msg":"✅ Success","level":"info"}
{"jobID":"build-app","step":"build","msg":"Build application","level":"info"}
{"jobID":"build-app","step":"build","stepResult":"success","msg":"✅ Success","level":"info"}
{"jobID":"build-app","jobResult":"success","msg":"🏁 Job succeeded","level":"info"}
{"jobID":"deploy-cdk","msg":"⭐ Run Set up job","level":"info"}
{"jobID":"deploy-cdk","jobResult":"skipped","msg":"🏁 Job succeeded","level":"info"}`

	result, err := ParseActOutput(jsonOutput)
	if err != nil {
		t.Fatalf("ParseActOutput failed: %v", err)
	}

	if result.Jobs["build-app"] == nil {
		t.Fatal("expected build-app job in result")
	}
	if result.Jobs["build-app"].Conclusion != "success" {
		t.Errorf("expected build-app conclusion=success, got %s", result.Jobs["build-app"].Conclusion)
	}
	if len(result.Jobs["build-app"].Steps) != 2 {
		t.Errorf("expected 2 steps in build-app, got %d", len(result.Jobs["build-app"].Steps))
	}
	if result.Jobs["deploy-cdk"] == nil {
		t.Fatal("expected deploy-cdk job in result")
	}
	if result.Jobs["deploy-cdk"].Conclusion != "skipped" {
		t.Errorf("expected deploy-cdk conclusion=skipped, got %s", result.Jobs["deploy-cdk"].Conclusion)
	}
}
