package canonical

import "github.com/fatjonblp/coding_challange_integrations/internal/importer"

// init registers the three canonical formats.
//
// Registration happens in an init function and nowhere else, so the only way to
// make a format available is to import the package that holds it. That is what
// makes internal/importer/all the single, greppable list of everything the twin
// can read: a format that is not blank-imported there is not linked in, and a
// format that is cannot be missed.
func init() {
	importer.Register(NewCSV())
	importer.Register(NewXML())
	importer.Register(NewJSON())
}
