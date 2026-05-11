package github

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/github/github-mcp-server/pkg/inventory"
	"github.com/github/github-mcp-server/pkg/utils"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var primaryCSVRowKeys = []string{
	"items",
	"issues",
	"discussions",
	"categories",
	"labels",
	"alerts",
	"advisories",
	"notifications",
	"gists",
	"repositories",
	"commits",
	"branches",
	"tags",
	"releases",
	"users",
	"teams",
	"members",
	"projects",
}

func withCSVOutputVariants(tools []inventory.ServerTool) []inventory.ServerTool {
	result := make([]inventory.ServerTool, 0, len(tools))
	for _, tool := range tools {
		if !isCSVOutputTool(tool) {
			result = append(result, tool)
			continue
		}

		jsonOnly := tool
		jsonOnly.FeatureFlagDisable = FeatureFlagCSVOutput
		result = append(result, jsonOnly)

		csvCapable := tool
		csvCapable.FeatureFlagEnable = FeatureFlagCSVOutput
		csvCapable.HandlerFunc = wrapHandlerWithCSVOutput(tool.HandlerFunc)
		result = append(result, csvCapable)
	}
	return result
}

func isCSVOutputTool(tool inventory.ServerTool) bool {
	if !tool.Toolset.Default {
		return false
	}
	if !strings.HasPrefix(tool.Tool.Name, "list_") {
		return false
	}
	return tool.FeatureFlagEnable == "" && tool.FeatureFlagDisable == ""
}

func wrapHandlerWithCSVOutput(next inventory.HandlerFunc) inventory.HandlerFunc {
	return func(deps any) mcp.ToolHandler {
		handler := next(deps)
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := handler(ctx, req)
			if err != nil || result == nil || result.IsError {
				return result, err
			}

			return convertJSONTextResultToCSV(result), nil
		}
	}
}

func convertJSONTextResultToCSV(result *mcp.CallToolResult) *mcp.CallToolResult {
	if len(result.Content) != 1 {
		return utils.NewToolResultError("failed to convert response to CSV: expected a single text content response")
	}

	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return utils.NewToolResultError("failed to convert response to CSV: expected a text content response")
	}

	csvText, err := jsonTextToCSV(text.Text)
	if err != nil {
		return utils.NewToolResultErrorFromErr("failed to convert response to CSV", err)
	}

	resultCopy := *result
	resultCopy.Content = []mcp.Content{&mcp.TextContent{Text: csvText}}
	resultCopy.StructuredContent = nil
	return &resultCopy
}

func jsonTextToCSV(text string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON text: %w", err)
	}

	rows := csvRows(value)
	if len(rows) == 0 {
		return "", nil
	}

	headers := csvHeaders(rows)
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write(headers); err != nil {
		return "", fmt.Errorf("failed to write CSV header: %w", err)
	}

	for _, row := range rows {
		record := make([]string, len(headers))
		for i, header := range headers {
			record[i] = row[header]
		}
		if err := writer.Write(record); err != nil {
			return "", fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("failed to flush CSV: %w", err)
	}
	return buf.String(), nil
}

func csvRows(value any) []map[string]string {
	switch v := value.(type) {
	case []any:
		return csvRowsFromArray(v, nil)
	case map[string]any:
		if rows, metadata, ok := primaryRowsFromMap(v); ok {
			return csvRowsFromArray(rows, metadata)
		}
		return []map[string]string{newFlattenedCSVRow(v)}
	default:
		return []map[string]string{{"value": scalarCSVValue(v)}}
	}
}

func primaryRowsFromMap(value map[string]any) ([]any, map[string]any, bool) {
	if key, ok := preferredPrimaryRowKey(value); ok {
		rows, _ := value[key].([]any)
		return rows, metadataWithoutKey(value, key), true
	}

	var arrayKeys []string
	for key, raw := range value {
		if _, ok := raw.([]any); ok {
			arrayKeys = append(arrayKeys, key)
		}
	}
	if len(arrayKeys) != 1 {
		return nil, nil, false
	}

	key := arrayKeys[0]
	rows, _ := value[key].([]any)
	return rows, metadataWithoutKey(value, key), true
}

func preferredPrimaryRowKey(value map[string]any) (string, bool) {
	for _, key := range primaryCSVRowKeys {
		if _, ok := value[key].([]any); ok {
			return key, true
		}
	}
	return "", false
}

func metadataWithoutKey(value map[string]any, exclude string) map[string]any {
	metadata := make(map[string]any, len(value)-1)
	for key, raw := range value {
		if key != exclude {
			metadata[key] = raw
		}
	}
	return metadata
}

func csvRowsFromArray(values []any, metadata map[string]any) []map[string]string {
	if len(values) == 0 {
		return nil
	}

	rows := make([]map[string]string, 0, len(values))
	metadataRow := newFlattenedCSVRow(metadata)
	for _, value := range values {
		row := make(map[string]string, len(metadataRow))
		maps.Copy(row, metadataRow)

		switch v := value.(type) {
		case map[string]any:
			appendFlattenedCSVFields(row, v, "")
		default:
			row["value"] = scalarCSVValue(v)
		}
		rows = append(rows, row)
	}
	return rows
}

func newFlattenedCSVRow(value map[string]any) map[string]string {
	row := make(map[string]string)
	appendFlattenedCSVFields(row, value, "")
	return row
}

func appendFlattenedCSVFields(row map[string]string, value map[string]any, prefix string) {
	if value == nil {
		return
	}

	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		column := csvColumnName(prefix, key)
		raw := value[key]
		switch v := raw.(type) {
		case map[string]any:
			appendFlattenedCSVFields(row, v, column)
		case []any:
			row[column] = csvArrayValue(v)
		default:
			row[column] = csvColumnValue(column, v)
		}
	}
}

func csvHeaders(rows []map[string]string) []string {
	headerSet := make(map[string]struct{})
	for _, row := range rows {
		for header := range row {
			headerSet[header] = struct{}{}
		}
	}

	headers := make([]string, 0, len(headerSet))
	for header := range headerSet {
		headers = append(headers, header)
	}
	sort.Strings(headers)
	return headers
}

func csvColumnName(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func csvColumnValue(column string, value any) string {
	str := scalarCSVValue(value)
	if isBodyColumn(column) {
		return normalizeCSVWhitespace(str)
	}
	return str
}

func csvArrayValue(values []any) string {
	if len(values) == 0 {
		return ""
	}

	// Scalar arrays use semicolons for compactness. This is lossy if an
	// element contains a semicolon; use JSON mode when exact reconstruction matters.
	parts := make([]string, 0, len(values))
	for _, value := range values {
		switch value.(type) {
		case map[string]any, []any:
			encoded, err := json.Marshal(value)
			if err != nil {
				parts = append(parts, scalarCSVValue(value))
			} else {
				parts = append(parts, string(encoded))
			}
		default:
			parts = append(parts, scalarCSVValue(value))
		}
	}
	return strings.Join(parts, ";")
}

func scalarCSVValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

func isBodyColumn(column string) bool {
	return column == "body" || strings.HasSuffix(column, ".body")
}

func normalizeCSVWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
