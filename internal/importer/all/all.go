// Package all links every importer the twin can read into the binary. It has no
// API: importing it for effect is the whole point.
//
// The twin's mains blank-import this package and then look formats up by id
// through [importer.Lookup], so the list of available formats is this file and
// nothing else. A format registers itself from an init function, so a format that
// is not imported here does not exist at run time, and one that is cannot be
// forgotten.
//
//	import _ "github.com/fatjonblp/coding_challange_integrations/internal/importer/all"
package all

import (
	// The three canonical profiles the twin ships: blp-csv-v1, blp-xml-v1 and
	// blp-json-v1.
	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/canonical"
	// The legacy KRED-EXP 2.1 profile BLP's capture reads the subsidiary's invoices with.
	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/kredexp"
)
