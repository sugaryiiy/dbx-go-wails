package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sugaryiiy/magpiedb/internal/database"
)

func TestExportUsesTypedRowsAndPreservesNumbers(t *testing.T) {
	input := database.QueryResult{Columns: []string{"id", "amount", "flag"}, Rows: [][]any{{int64(42), json.Number("12.500000000000000001"), true}}}
	q, err := exportQueryResult(input)
	if err != nil {
		t.Fatal(err)
	}
	if &q.Rows[0][0] != &input.Rows[0][0] {
		t.Fatal("export copied the typed query rows")
	}
	data, _, err := encodeTransfer(q, "json", nil)
	if err != nil || !strings.Contains(string(data), `"amount":12.500000000000000001`) || !strings.Contains(string(data), `"id":42`) {
		t.Fatalf("numeric export: %s %v", data, err)
	}
	data, _, err = encodeTransfer(q, "xlsx", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, _, err := parseTransferFile(data, "xlsx", nil)
	if err != nil || rows[0][0] != "42" || rows[0][1] != "12.500000000000000001" {
		t.Fatalf("XLSX numeric export: %#v %v", rows, err)
	}
}

func TestExportJSONRetainsDuplicateAndShortRowSemantics(t *testing.T) {
	q := exportResult{Columns: []string{"x", "x", "y"}, Rows: [][]any{{"first", "last", nil}, {"short"}}}
	data, _, err := encodeTransfer(q, "json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["x"] != "last" || rows[1]["x"] != "short" || len(rows[1]) != 1 {
		t.Fatal(rows)
	}
	data, _, err = encodeTransfer(exportResult{}, "json", nil)
	if err != nil || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("empty export: %s %v", data, err)
	}
}

type cancellingExportValue struct{ cancel context.CancelFunc }

func (v cancellingExportValue) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`"cancelled"`), nil
}

func TestExportEncodingHonorsCancellationAndByteBudget(t *testing.T) {
	for _, format := range []string{"csv", "txt", "json", "xlsx"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := encodeTransferContext(ctx, exportResult{}, format, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s ignored cancellation: %v", format, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, _, err := encodeTransferContext(ctx, exportResult{Columns: []string{"x"}, Rows: [][]any{{cancellingExportValue{cancel}}}}, "json", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-progress encoding ignored cancellation: %v", err)
	}
	q := exportResult{Columns: []string{"x"}, Rows: [][]any{{strings.Repeat("x", maxTransferBytes)}}}
	for _, format := range []string{"csv", "json"} {
		if _, _, err := encodeTransfer(q, format, nil); err == nil || !strings.Contains(err.Error(), "32 MiB") {
			t.Fatalf("%s ignored output limit: %v", format, err)
		}
	}
}

func TestExportAcceptsAgentRowsWithoutJSONRoundTrip(t *testing.T) {
	row := []any{json.Number("9007199254740993"), nil}
	q, err := exportQueryResult(map[string]any{"columns": []any{"id", "note"}, "rows": []any{row}, "truncated": true})
	if err != nil || !q.Truncated || &q.Rows[0][0] != &row[0] {
		t.Fatalf("agent result: %#v %v", q, err)
	}
	for _, result := range []any{nil, map[string]any{"columns": []any{1}}, map[string]any{"columns": []string{"x"}, "rows": []any{"bad"}}} {
		if _, err := exportQueryResult(result); err == nil {
			t.Fatalf("accepted malformed result: %#v", result)
		}
	}
}

func TestExportCSVLongFieldCannotBypassByteBudget(t *testing.T) {
	buffer := &transferBuffer{ctx: context.Background()}
	writer := csv.NewWriter(buffer)
	err := writer.Write([]string{strings.Repeat("x", maxTransferBytes+4096)})
	writer.Flush()
	if err == nil {
		err = writer.Error()
	}
	if err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("long CSV field ignored byte budget: %v", err)
	}
	if buffer.Len() > maxTransferBytes {
		t.Fatalf("CSV field grew buffer past byte budget: %d", buffer.Len())
	}
}

func BenchmarkExportJSONTypedRows(b *testing.B) {
	q := database.QueryResult{Columns: make([]string, 10), Rows: make([][]any, 10000)}
	for i := range q.Columns {
		q.Columns[i] = fmt.Sprintf("c%d", i)
	}
	for i := range q.Rows {
		q.Rows[i] = make([]any, 10)
		for j := range q.Rows[i] {
			q.Rows[i][j] = "01234567890123456789"
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := exportQueryResult(q)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err := encodeTransfer(result, "json", nil); err != nil {
			b.Fatal(err)
		}
	}
}
