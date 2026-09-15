package simclock

// An EndpointClass names a row of the published virtual-cost table. It is the
// only place the cost of a request is defined; handlers pass the class and the
// record count to Cost and hand the result to Governor.Admit.
type EndpointClass string

// The endpoint classes of the virtual-cost table.
const (
	// ERPListPage is an ERP list page: 40 vms + 0.5 vms per record returned.
	ERPListPage EndpointClass = "erp_list_page"
	// ERPSingleGet is an ERP single-resource GET: 25 vms.
	ERPSingleGet EndpointClass = "erp_single_get"
	// ERPSoapRateTable is the SOAP GetExchangeRateTable operation:
	// 120 vms + 0.2 vms per row.
	ERPSoapRateTable EndpointClass = "erp_soap_rate_table"
	// ERPDocumentPost is a single ERP document POST: 60 vms.
	ERPDocumentPost EndpointClass = "erp_document_post"
	// ERPBatchDocumentPost is a batch ERP document POST: 60 vms + 8 vms per item.
	ERPBatchDocumentPost EndpointClass = "erp_batch_document_post"
	// ERPAuthToken is the ERP token endpoint: 10 vms.
	ERPAuthToken EndpointClass = "erp_auth_token"
	// TwinOpenBatch opens a twin batch: 30 vms.
	TwinOpenBatch EndpointClass = "twin_open_batch"
	// TwinRecordsChunk is a twin records chunk: 20 vms + 0.15 vms per record.
	TwinRecordsChunk EndpointClass = "twin_records_chunk"
	// TwinCommit is a twin batch commit or abort: 20 vms.
	TwinCommit EndpointClass = "twin_commit"
	// TwinSinglePut is a twin single-record PUT: 20 vms.
	TwinSinglePut EndpointClass = "twin_single_put"
	// TwinOutboxList is a twin outbox list page: 30 vms + 0.1 vms per record.
	TwinOutboxList EndpointClass = "twin_outbox_list"
	// TwinAcks is a twin ack submission: 20 vms + 0.05 vms per ack.
	TwinAcks EndpointClass = "twin_acks"
	// TwinAuthToken is the twin token endpoint: 10 vms.
	TwinAuthToken EndpointClass = "twin_auth_token"
)

// Cost returns base_vms100 + per_record_vms100*records for class, in hundredths
// of a virtual millisecond. A negative records count is treated as zero. An
// unrecognized class costs nothing; the cost table is closed, so reaching that
// branch is a programming error the caller is expected to notice as a
// suspiciously free request rather than as a panic inside a handler.
func Cost(class EndpointClass, records int) int64 {
	base, perRecord := costRow(class)
	if records < 0 {
		records = 0
	}
	return base + perRecord*int64(records)
}

// costRow returns the base and per-record cost of class in vms100. It is a
// switch rather than a map so that no map iteration is involved anywhere in the
// cost path.
func costRow(class EndpointClass) (base, perRecord int64) {
	switch class {
	case ERPListPage:
		return 4000, 50
	case ERPSingleGet:
		return 2500, 0
	case ERPSoapRateTable:
		return 12000, 20
	case ERPDocumentPost:
		return 6000, 0
	case ERPBatchDocumentPost:
		return 6000, 800
	case ERPAuthToken:
		return 1000, 0
	case TwinOpenBatch:
		return 3000, 0
	case TwinRecordsChunk:
		return 2000, 15
	case TwinCommit:
		return 2000, 0
	case TwinSinglePut:
		return 2000, 0
	case TwinOutboxList:
		return 3000, 10
	case TwinAcks:
		return 2000, 5
	case TwinAuthToken:
		return 1000, 0
	}
	return 0, 0
}
