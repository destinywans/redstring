package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeploymentImageProjectionOmitsEnvironment(t *testing.T) {
	images := templateImages(json.RawMessage(`{"metadata":{"annotations":{"secret":"private"}},"spec":{"containers":[{"name":"app","image":"app:v1","env":[{"name":"PASSWORD","value":"private"}]}]}}`))
	b, err := json.Marshal(images)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `[{"name":"app","image":"app:v1"}]` || strings.Contains(string(b), "private") {
		t.Fatalf("unexpected projection %s", b)
	}
}
