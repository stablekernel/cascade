package config

// boolPtr and intPtr build pointers to literal values for tests that populate
// the pointer-typed inheritable override fields (see the deep-merge inheritance
// contract on PRPreviewConfig).
func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }
