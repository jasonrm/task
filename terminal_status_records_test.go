//go:build darwin || linux

package task_test

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type terminalRecord struct {
	State, ID, Title, Message, Kind, Sequence string
}

func terminalRecords(t *testing.T, output string) []terminalRecord {
	t.Helper()
	var records []terminalRecord
	for _, sequence := range regexp.MustCompile(`\x1b\]7501;[^\x07]*\x07`).FindAllString(output, -1) {
		values := map[string]string{}
		for _, pair := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b]7501;"), "\x07"), ":") {
			k, v, ok := strings.Cut(pair, "=")
			require.True(t, ok)
			if k == "title" || k == "msg" {
				decoded, err := base64.StdEncoding.DecodeString(v)
				require.NoError(t, err)
				v = string(decoded)
			}
			values[k] = v
		}
		if values["state"] == "clear" {
			require.NotEmpty(t, values["id"], "must never clear the terminal's root")
		}
		records = append(records, terminalRecord{State: values["state"], ID: values["id"], Title: values["title"], Message: values["msg"], Kind: values["kind"], Sequence: sequence})
	}
	return records
}

func rootRecords(t *testing.T, output string) []terminalRecord {
	t.Helper()
	var roots []terminalRecord
	for _, r := range terminalRecords(t, output) {
		if r.ID == "" {
			roots = append(roots, r)
		}
	}
	return roots
}

func remainingRecords(t *testing.T, output string) map[string]terminalRecord {
	t.Helper()
	records := map[string]terminalRecord{}
	for _, r := range terminalRecords(t, output) {
		if r.State == "clear" {
			for id := range records {
				if id == r.ID || strings.HasPrefix(id, r.ID+"/") {
					delete(records, id)
				}
			}
		} else {
			records[r.ID] = r
		}
	}
	return records
}
