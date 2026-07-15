package txm

// ptr is a test helper duplicated from relayer/config (where the production
// copy now lives). Kept here so txm test files can construct config.Config
// literals without importing the unexported helper across packages.
func ptr[T any](v T) *T { return &v }
