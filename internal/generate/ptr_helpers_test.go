package generate

// intPtr builds a pointer to a literal int for tests that populate the
// pointer-typed inheritable override fields (e.g. ValidateConfig.Retries).
// boolPtr lives in deploy_strategy_test.go.
func intPtr(i int) *int { return &i }
