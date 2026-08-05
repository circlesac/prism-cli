package secret

import (
	"bytes"
	"testing"
)

func TestPipedSecretsAreReadOneLineAtATime(t *testing.T) {
	input := bytes.NewBufferString("first-secret\nsecond-secret\n")
	first, err := Read("", input, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Read("", input, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if first != "first-secret" || second != "second-secret" {
		t.Fatalf("secrets = %q, %q", first, second)
	}
}

func TestRequiredSecretRejectsEmptyInput(t *testing.T) {
	if _, err := Read("", bytes.NewBuffer(nil), nil, true); err == nil {
		t.Fatal("empty required credential was accepted")
	}
}
