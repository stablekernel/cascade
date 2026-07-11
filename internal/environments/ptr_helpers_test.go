package environments

// intPtr builds a pointer to a literal int for tests that populate the
// pointer-typed config.EnvironmentConfig.WaitTimer field.
func intPtr(i int) *int { return &i }
