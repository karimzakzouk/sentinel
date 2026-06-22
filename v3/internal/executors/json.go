package executors

import "encoding/json"

// jsonMarshalImpl is split out to avoid an import cycle in tests.
func jsonMarshalImpl(v any) ([]byte, error) {
	return json.Marshal(v)
}
