package erp

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// Published operating parameters of the ERP, from sections 3.2, 4 and 7.1 of the
// build specification. They are constants rather than settings: a candidate
// reads them in the spec and must be able to rely on them.
const (
	// BucketCapacity is the token bucket capacity.
	BucketCapacity = 40
	// BucketRefillIntervalVms earns one token per this many virtual
	// milliseconds. It is also the virtual cost the ERP charges for a 429.
	BucketRefillIntervalVms = 50
	// TokenRequestTTL is the number of authenticated requests an access token
	// survives.
	TokenRequestTTL = 250
	// TokenVirtualTTLVms is the virtual-millisecond lifetime of an access token.
	TokenVirtualTTLVms = 900_000

	// DefaultListLimit is the page size a list request gets when it asks for
	// none.
	DefaultListLimit = 100
	// MaxListLimit is the largest page the ERP serves. A larger limit is
	// clamped silently, which is what a real ERP does, and the response says so
	// in HeaderLimitClamped.
	MaxListLimit = 250
	// MaxBatchItems is the largest document batch the ERP accepts.
	MaxBatchItems = 200

	// DefaultListenAddr is the address the service listens on for humans.
	DefaultListenAddr = "127.0.0.1:8082"
	// DefaultExportDir is the file export drop, simulating the sender's SFTP
	// directory.
	DefaultExportDir = "var/erp/export"
	// DefaultRequestLogLimit is how many request log entries the admin surface
	// retains. It is far above any published quota, so a scenario never loses
	// one.
	DefaultRequestLogLimit = 20_000
)

// HeaderLimitClamped is set to the effective page size when a client asked for
// more than MaxListLimit. Clamping is not an error; the header is the ERP's
// concession to fairness.
const HeaderLimitClamped = "X-Limit-Clamped"

// Credentials are the two credential pairs of one seeded landscape: client
// credentials for the REST surface and a WS-Security UsernameToken for the SOAP
// channel. One landscape with two auth models is the single most common surprise
// in a first ERP integration, so the challenge ships it on purpose.
//
// The values are derived from the scenario seed, never drawn at random, so the
// grader can compute them without a fixture file and two runs of one scenario
// issue the same credentials.
type Credentials struct {
	// ClientID and ClientSecret authenticate POST /erp/v1/auth/token.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// SOAPUsername and SOAPPassword go into the wsse:UsernameToken of the SOAP
	// request. They are deliberately not the REST credentials.
	SOAPUsername string `json:"soap_username"`
	SOAPPassword string `json:"soap_password"`
	// AdminToken gates the /erp-admin/v1 surface. It is never issued to a
	// connector.
	AdminToken string `json:"admin_token"`
}

// Fixed identities of the seeded landscape. Only the secrets vary with the seed.
const (
	// ClientID is the connector's client identifier on the REST surface.
	ClientID = "blp-connector"
	// SOAPUsername is the WS-Security user of the SOAP channel.
	SOAPUsername = "FINREF_SVC"
	// Tenant is the tenant every posting is booked under. The ERP indexes
	// duplicates by (tenant, external_reference), and this landscape has one
	// tenant.
	Tenant = "acme-ch"
)

// CredentialsFor returns the credential set of a seed. It is a pure function:
// the grader, the server and a test all derive the same values from the same
// seed, which is why no credential fixture file exists.
func CredentialsFor(seed int64) Credentials {
	return Credentials{
		ClientID:     ClientID,
		ClientSecret: derive("erp/client-secret", seed, "cs_"),
		SOAPUsername: SOAPUsername,
		SOAPPassword: derive("erp/soap-password", seed, "sp_"),
		AdminToken:   derive("erp/admin-token", seed, "at_"),
	}
}

// derive returns prefix followed by 32 hex characters of sha256 over the domain
// and the seed. It is a key derivation, not a random draw: the same seed always
// yields the same secret.
func derive(domain string, seed int64, prefix string) string {
	sum := sha256.Sum256([]byte(domain + "|" + strconv.FormatInt(seed, 10)))
	return prefix + hex.EncodeToString(sum[:])[:32]
}

// Config configures a [Server]. Every field has a documented zero value, so a
// zero Config plus a scenario and a seed is a working smoke server.
type Config struct {
	// Scenario and Seed select the dataset. An empty Scenario is
	// seed.ScenarioS0.
	Scenario string
	// Seed keys the dataset, the credentials and fault injection.
	Seed int64
	// Quota is the hard per-run request quota. Non-positive means unlimited,
	// which is what a human running `make up` wants and never what grading
	// uses.
	Quota int
	// Chaos enables the Governor's content-addressed 429 and 503 injection.
	// It does not affect the SOAP channel's own two faults, which are static
	// properties of that channel: the CH20 permanent fault and the retryable
	// first delivery of the CH10 signature are graded in every scenario.
	Chaos bool
	// ExportDir is where the legacy file drop is written on seed and on reset.
	// Empty disables the drop, which is what a REST-only test wants.
	ExportDir string
	// Creds overrides the seed-derived credentials. A zero field falls back to
	// CredentialsFor(Seed), so a caller can override the admin token alone.
	Creds Credentials
	// SOAPTruncateAt cuts the exchange rate table at this many rows and reports
	// Truncated=true in the SOAP response header. Zero serves the whole table.
	// It is the knob the FX-edges scenario uses; meaning lives in the header,
	// and a correct client refuses to post on a truncated table.
	SOAPTruncateAt int

	// PermanentFaultAt answers the nth non-admin request of the run with a
	// permanent, non-retriable transport error: a 503 whose retriable flag is
	// false. Zero disables it. It is a count and not a hash because a permanent
	// failure changes what a correct run achieves, so which request it lands on
	// is something a scenario states rather than something a seed decides.
	PermanentFaultAt int

	// PermanentFaultPath narrows the permanent fault to requests whose path
	// contains this substring. Empty matches any non-admin request.
	PermanentFaultPath string

	// EmptyPageAt returns one deliberately EMPTY page on the nth page request of
	// every paginated collection, with has_more still true and a usable cursor.
	// Zero disables it.
	//
	// An empty page is not the end of a collection: has_more is the authority on
	// that, and this is what makes the difference observable. Real systems do
	// this - a page whose records were all filtered out after the cursor was
	// issued - and a client that stops on it loses everything after it.
	EmptyPageAt int
	// RequestLogLimit caps the admin request log. Non-positive means
	// DefaultRequestLogLimit.
	RequestLogLimit int
	// Log is where the one-line JSON request log goes. Nil means os.Stdout.
	Log io.Writer
	// UI is an optional web UI mounted under /ui/. It is free of charge and
	// never authenticated, exactly like the admin surface is free but unlike it
	// is gated. Nil mounts nothing.
	UI http.Handler
}

// withDefaults returns cfg with every documented default applied.
func (cfg Config) withDefaults() Config {
	out := cfg
	if out.Scenario == "" {
		out.Scenario = seed.ScenarioS0
	}
	derived := CredentialsFor(out.Seed)
	if out.Creds.ClientID == "" {
		out.Creds.ClientID = derived.ClientID
	}
	if out.Creds.ClientSecret == "" {
		out.Creds.ClientSecret = derived.ClientSecret
	}
	if out.Creds.SOAPUsername == "" {
		out.Creds.SOAPUsername = derived.SOAPUsername
	}
	if out.Creds.SOAPPassword == "" {
		out.Creds.SOAPPassword = derived.SOAPPassword
	}
	if out.Creds.AdminToken == "" {
		out.Creds.AdminToken = derived.AdminToken
	}
	if out.RequestLogLimit <= 0 {
		out.RequestLogLimit = DefaultRequestLogLimit
	}
	return out
}
