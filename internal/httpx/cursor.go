package httpx

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// cursorVersion is the domain-separation prefix of the cursor signature. It is
// part of the signed material, so a cursor from a future format can never be
// mistaken for a valid one of this format.
const cursorVersion = "httpx/cursor/v1"

// cursorSigLen is the hex length of the truncated sha256 in a cursor: 8 bytes.
// A pagination cursor is not a capability token, and 64 bits of tamper evidence
// against a local, sequential, quota-limited client is ample; the shorter
// signature keeps the cursor readable in a log line.
const cursorSigLen = 16

// A Cursor is an opaque, stable, tamper-evident pagination position. It is the
// only pagination state on the wire on both servers.
//
// Encode renders (dataset, last_key, change_seq_high) as base64url of canonical
// JSON with a truncated sha256 of a server secret appended as sig. Properties
// that both servers depend on:
//
//   - Deterministic: the same logical position always yields the same string,
//     because the payload carries no counter, no nonce and no clock. Two runs of
//     the same scenario emit byte-identical cursors, which is what makes a
//     transcript diffable.
//   - Opaque: a client that parses a cursor is relying on a shape we do not
//     promise. Nothing but this package reads the inside of one.
//   - Tamper-evident: a hand-edited cursor fails the signature check and is
//     400 INVALID_CURSOR, so a client cannot page past a filter or forge a
//     watermark by editing a base64 string.
type Cursor struct {
	// Dataset is the dataset the cursor pages over. It is part of the signed
	// material, so a supplier cursor cannot be replayed on purchase orders.
	Dataset string `json:"dataset"`
	// LastKey is the natural key of the last record already returned.
	LastKey string `json:"last_key"`
	// ChangeSeqHigh is the change sequence of the last record already
	// returned. Ordering is by (change_seq, natural_key), so the pair is the
	// full position.
	ChangeSeqHigh int64 `json:"change_seq_high"`
}

// IsZero reports whether c is the start-of-dataset position.
func (c Cursor) IsZero() bool { return c == Cursor{} }

// cursorEnvelope is the wire shape of a cursor: the payload plus its signature,
// with the members in the sorted order canonical JSON requires. Struct order is
// the encoding order, so no map iteration is involved.
type cursorEnvelope struct {
	ChangeSeqHigh int64  `json:"change_seq_high"`
	Dataset       string `json:"dataset"`
	LastKey       string `json:"last_key"`
	Sig           string `json:"sig"`
}

// Encode renders c as an opaque cursor string signed with secret. The result is
// URL- and header-safe: unpadded base64url of compact JSON.
func (c Cursor) Encode(secret []byte) string {
	env := cursorEnvelope{
		ChangeSeqHigh: c.ChangeSeqHigh,
		Dataset:       c.Dataset,
		LastKey:       c.LastKey,
		Sig:           cursorSig(c, secret),
	}
	// The envelope has only scalar members, so Marshal cannot fail.
	buf, _ := json.Marshal(env)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// ParseCursor decodes and verifies a cursor string. The empty string is the
// start-of-dataset position and is not an error, so a caller can pass a missing
// query parameter straight through. Anything else that is not a cursor this
// server signed is 400 INVALID_CURSOR: bad base64, bad JSON, unknown members,
// or a signature that does not match.
func ParseCursor(s string, secret []byte) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	buf, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		// Tolerate a padded cursor: a client that round-tripped it through a
		// form encoder has not tampered with it.
		buf, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return Cursor{}, InvalidCursor()
		}
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	var env cursorEnvelope
	if err := dec.Decode(&env); err != nil {
		return Cursor{}, InvalidCursor()
	}
	if dec.More() {
		return Cursor{}, InvalidCursor()
	}
	c := Cursor{
		Dataset:       env.Dataset,
		LastKey:       env.LastKey,
		ChangeSeqHigh: env.ChangeSeqHigh,
	}
	want := cursorSig(c, secret)
	if subtle.ConstantTimeCompare([]byte(env.Sig), []byte(want)) != 1 {
		return Cursor{}, InvalidCursor()
	}
	return c, nil
}

// cursorSig returns the truncated hex sha256 over the version prefix, the
// secret and the canonical payload. Every field is length-prefixed by its own
// separator, so two different positions can never produce the same signed
// material.
func cursorSig(c Cursor, secret []byte) string {
	h := sha256.New()
	h.Write([]byte(cursorVersion))
	h.Write([]byte{0x1f})
	h.Write(secret)
	h.Write([]byte{0x1f})
	h.Write([]byte(c.Dataset))
	h.Write([]byte{0x1f})
	h.Write([]byte(c.LastKey))
	h.Write([]byte{0x1f})
	h.Write([]byte(strconv.FormatInt(c.ChangeSeqHigh, 10)))
	return hex.EncodeToString(h.Sum(nil))[:cursorSigLen]
}
