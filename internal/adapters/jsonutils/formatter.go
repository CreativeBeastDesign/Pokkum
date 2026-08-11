package jsonutils

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage declares that jsonutils is a helper module and not a direct port adapter.
const IsUtilityPackage = true

const CurrentSchemaVersion = "1.0"

// FormatSuccess wraps data into a standard Pokkum JSON envelope.
func FormatSuccess(command string, data interface{}) ([]byte, error) {
	env := ports.JSONEnvelope{
		SchemaVersion: CurrentSchemaVersion,
		Command:       command,
		Status:        "success",
		Data:          data,
	}
	return json.MarshalIndent(env, "", "  ")
}

// FormatError wraps error information into a standard Pokkum JSON envelope.
func FormatError(command string, code string, message string, details string) ([]byte, error) {
	env := ports.JSONEnvelope{
		SchemaVersion: CurrentSchemaVersion,
		Command:       command,
		Status:        "error",
		Error: &ports.ErrorData{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
	return json.MarshalIndent(env, "", "  ")
}

// WriteSuccess writes a JSON success envelope directly to an io.Writer.
func WriteSuccess(w io.Writer, command string, data interface{}) error {
	bytes, err := FormatSuccess(command, data)
	if err != nil {
		return fmt.Errorf("jsonutils adapter: failed to marshal success payload: %w", err)
	}
	_, err = fmt.Fprintln(w, string(bytes))
	return err
}

// WriteError writes a JSON error envelope directly to an io.Writer.
func WriteError(w io.Writer, command string, code string, message string, details string) error {
	bytes, err := FormatError(command, code, message, details)
	if err != nil {
		return fmt.Errorf("jsonutils adapter: failed to marshal error payload: %w", err)
	}
	_, err = fmt.Fprintln(w, string(bytes))
	return err
}
