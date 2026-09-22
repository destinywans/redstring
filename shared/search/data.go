// Package searchdata embeds the same single-character pinyin dictionary used by
// the browser and PostgreSQL. Regenerate it with scripts/generate-search-dictionary.go.
package searchdata

import (
	_ "embed"
	"encoding/json"
)

//go:embed pinyin.json
var dictionaryJSON []byte

// Pinyin is generated from go-pinyin v0.21.0; treat it as read-only.
var Pinyin = func() map[string]string {
	var dictionary map[string]string
	if err := json.Unmarshal(dictionaryJSON, &dictionary); err != nil {
		panic("invalid embedded search dictionary: " + err.Error())
	}
	return dictionary
}()
