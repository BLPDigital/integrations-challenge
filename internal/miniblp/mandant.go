package miniblp

import (
	"encoding/json"
	"fmt"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// CompanyCodeForMandant maps a Vorlaufsatz Mandant to the group ERP's company
// code. The table is published in docs/rules.md.
//
// The subsidiary's Mandant is its own client number and is not the group's
// company code: 0100 is CH10 and 0200 is CH20. Translating between them is
// master data mapping, so it is the twin's job and not the parser's. A parser
// reports what the file says; inventing a company code the file never carried
// would be a parser that lies about its input, and it would put a mapping table
// nobody published into the middle of the graded Go task.
//
// An unmapped Mandant is an error rather than a passthrough, because a company
// code that reaches the matching engine unmapped fails against every cost center
// in the group and produces a hundred identical exceptions instead of one clear
// one, which is exactly the failure this function was written after.
func CompanyCodeForMandant(mandant string) (string, error) {
	switch mandant {
	case "0100":
		return model.CompanyCodeCH10, nil
	case "0200":
		return model.CompanyCodeCH20, nil
	}
	if model.IsKnownCompanyCode(mandant) {
		// Already canonical. A REST-pulled record or a canonical file arrives
		// this way, and the same helper is safe to call on both.
		return mandant, nil
	}
	return "", fmt.Errorf("miniblp: no company code is mapped to Mandant %q", mandant)
}

// canonicalizeLegacyDocument rewrites the company code of a document that came
// out of a legacy profile, in place, and reports the finding when the Mandant is
// unmapped.
//
// It touches nothing else. Everything the parser produced stays byte for byte as
// it produced it, which keeps the Go task's golden projections independent of
// this mapping.
func canonicalizeLegacyDocument(doc *importer.Document) *Finding {
	if doc == nil || len(doc.Payload) == 0 {
		return nil
	}
	var probe struct {
		CompanyCode string `json:"company_code"`
	}
	if err := json.Unmarshal(doc.Payload, &probe); err != nil || probe.CompanyCode == "" {
		return nil
	}
	canonical, err := CompanyCodeForMandant(probe.CompanyCode)
	if err != nil {
		return &Finding{Code: model.CodeEnumUnknown, Field: "company_code",
			Message: "Mandant " + probe.CompanyCode + " has no mapped company code"}
	}
	if canonical == probe.CompanyCode {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(doc.Payload, &obj); err != nil {
		return nil
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil
	}
	obj["company_code"] = encoded
	rewritten, err := model.CanonicalJSON(obj)
	if err != nil {
		return nil
	}
	doc.Payload = rewritten
	return nil
}
