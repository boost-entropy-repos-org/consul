package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hashicorp/consul/agent/structs"
)

// Query is a type used to query any single value index that may include an
// enterprise identifier.
type Query struct {
	Value string
	structs.EnterpriseMeta
}

// uuidStringToBytes is a modified version of memdb.UUIDFieldIndex.parseString
func uuidStringToBytes(uuid string) ([]byte, error) {
	l := len(uuid)
	if l != 36 {
		return nil, fmt.Errorf("UUID must be 36 characters")
	}

	hyphens := strings.Count(uuid, "-")
	if hyphens > 4 {
		return nil, fmt.Errorf(`UUID should have maximum of 4 "-"; got %d`, hyphens)
	}

	// The sanitized length is the length of the original string without the "-".
	sanitized := strings.Replace(uuid, "-", "", -1)
	sanitizedLength := len(sanitized)
	if sanitizedLength%2 != 0 {
		return nil, fmt.Errorf("UUID (without hyphens) must be even length")
	}

	dec, err := hex.DecodeString(sanitized)
	if err != nil {
		return nil, fmt.Errorf("invalid UUID: %w", err)
	}
	return dec, nil
}
