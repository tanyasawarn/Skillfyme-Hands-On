package validation

// PLAN.md K20: Result.Status's three real values (PASS/FAIL/ERROR --
// see this type's own doc comment) were bare string literals repeated
// ~35 times across handlers.go, with a 36th, independent copy in
// server.go's ExecValidator error path. A typo in any one of them
// wouldn't fail to compile -- it would just silently produce a status
// value nothing else recognizes.
const (
	StatusPass  = "PASS"
	StatusFail  = "FAIL"
	StatusError = "ERROR"
)
