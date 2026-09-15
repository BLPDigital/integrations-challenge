package miniblp

import "github.com/fatjonblp/coding_challange_integrations/internal/seed"

// seedExceptionCodes returns the exception codes the dataset generator can
// expect. It is a test-only bridge to internal/seed: the twin does not depend on
// the generator for its published code set, and one test asserts the two agree.
func seedExceptionCodes() []string { return seed.ExceptionCodes() }
