package secret

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func Read(prompt string, input io.Reader, output io.Writer, required bool) (string, error) {
	if output != nil {
		_, _ = fmt.Fprint(output, prompt)
	}
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		if output != nil {
			_, _ = fmt.Fprintln(output)
		}
		if err != nil {
			return "", errors.New("could not read the credential")
		}
		return validate(string(value), required)
	}
	var value bytes.Buffer
	buffer := make([]byte, 1)
	for {
		count, err := input.Read(buffer)
		if count == 1 {
			if buffer[0] == '\n' {
				break
			}
			value.WriteByte(buffer[0])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("could not read the credential")
		}
	}
	return validate(value.String(), required)
}

func validate(value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", errors.New("credential is required")
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("credential must be a single line")
	}
	return value, nil
}
