package github

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/github/github-mcp-server/pkg/inventory"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCSVOutputVariantsAreFeatureGated(t *testing.T) {
	listTool := testCSVOutputTool("list_things", `[{"number":1}]`)
	getTool := testCSVOutputTool("get_thing", `{"number":1}`)

	tools := withCSVOutputVariants([]inventory.ServerTool{listTool, getTool})
	require.Len(t, tools, 3)

	inv := buildCSVOutputInventory(t, tools, false)
	available := inv.AvailableTools(context.Background())
	require.Len(t, available, 2)

	jsonOnly := requireToolByName(t, available, "list_things")
	assert.Empty(t, jsonOnly.FeatureFlagEnable)
	assert.Equal(t, FeatureFlagCSVOutput, jsonOnly.FeatureFlagDisable)

	getThing := requireToolByName(t, available, "get_thing")
	assert.Empty(t, getThing.FeatureFlagEnable)
	assert.Empty(t, getThing.FeatureFlagDisable)

	inv = buildCSVOutputInventory(t, tools, true)
	available = inv.AvailableTools(context.Background())
	require.Len(t, available, 2)

	csvCapable := requireToolByName(t, available, "list_things")
	assert.Equal(t, FeatureFlagCSVOutput, csvCapable.FeatureFlagEnable)
	assert.Empty(t, csvCapable.FeatureFlagDisable)
}

func TestCSVOutputVariantDoesNotExposeFormatParameter(t *testing.T) {
	tools := withCSVOutputVariants([]inventory.ServerTool{testCSVOutputTool("list_things", `[{"number":1}]`)})
	csvCapable := requireCSVOutputVariant(t, tools)

	schema, ok := csvCapable.Tool.InputSchema.(*jsonschema.Schema)
	require.True(t, ok)
	assert.NotContains(t, schema.Properties, "output_format")
}

func TestCSVOutputVariantConvertsJSONTextToCSV(t *testing.T) {
	tools := withCSVOutputVariants([]inventory.ServerTool{
		testCSVOutputTool("list_things", `[
			{
				"number": 1,
				"body": "first line\n\tsecond line",
				"labels": ["bug", "help wanted"],
				"user": {"login": "octocat"}
			}
		]`),
	})
	inv := buildCSVOutputInventory(t, tools, true)
	csvCapable := requireToolByName(t, inv.AvailableTools(context.Background()), "list_things")

	result, err := csvCapable.Handler(nil)(context.Background(), testCSVOutputRequest())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)

	records := readCSVResult(t, result)
	require.Len(t, records, 2)

	row := csvRow(t, records[0], records[1])
	assert.Equal(t, "first line second line", row["body"])
	assert.Equal(t, "bug;help wanted", row["labels"])
	assert.Equal(t, "1", row["number"])
	assert.Equal(t, "octocat", row["user.login"])
}

func TestJSONOnlyVariantPreservesOriginalJSONText(t *testing.T) {
	const jsonResponse = `[{"number":1,"user":{"login":"octocat"}}]`
	tools := withCSVOutputVariants([]inventory.ServerTool{testCSVOutputTool("list_things", jsonResponse)})
	inv := buildCSVOutputInventory(t, tools, false)
	jsonOnly := requireToolByName(t, inv.AvailableTools(context.Background()), "list_things")

	result, err := jsonOnly.Handler(nil)(context.Background(), testCSVOutputRequest())
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.JSONEq(t, jsonResponse, text.Text)
}

func TestJSONTextToCSVFlattensPrimaryRows(t *testing.T) {
	csvText, err := jsonTextToCSV(`{
		"discussions": [
			{
				"number": 5,
				"title": "Discussion tools testing",
				"category": {"name": "Q&A"},
				"user": {"login": "octocat"}
			}
		]
	}`)
	require.NoError(t, err)

	reader := csv.NewReader(strings.NewReader(csvText))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)

	row := csvRow(t, records[0], records[1])
	assert.Equal(t, "Q&A", row["category.name"])
	assert.Equal(t, "5", row["number"])
	assert.Equal(t, "Discussion tools testing", row["title"])
	assert.Equal(t, "octocat", row["user.login"])
}

func testCSVOutputTool(name string, response string) inventory.ServerTool {
	return inventory.ServerTool{
		Tool: mcp.Tool{
			Name: name,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: true,
			},
			InputSchema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{},
			},
		},
		Toolset: ToolsetMetadataRepos,
		HandlerFunc: func(_ any) mcp.ToolHandler {
			return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: response},
					},
				}, nil
			}
		},
	}
}

func buildCSVOutputInventory(t *testing.T, tools []inventory.ServerTool, csvOutputEnabled bool) *inventory.Inventory {
	t.Helper()

	inv, err := inventory.NewBuilder().
		SetTools(tools).
		WithFeatureChecker(func(_ context.Context, flagName string) (bool, error) {
			return flagName == FeatureFlagCSVOutput && csvOutputEnabled, nil
		}).
		Build()
	require.NoError(t, err)
	return inv
}

func requireToolByName(t *testing.T, tools []inventory.ServerTool, name string) inventory.ServerTool {
	t.Helper()

	for _, tool := range tools {
		if tool.Tool.Name == name {
			return tool
		}
	}
	require.Failf(t, "tool not found", "tool %q not found", name)
	return inventory.ServerTool{}
}

func requireCSVOutputVariant(t *testing.T, tools []inventory.ServerTool) inventory.ServerTool {
	t.Helper()

	for _, tool := range tools {
		if tool.FeatureFlagEnable == FeatureFlagCSVOutput {
			return tool
		}
	}
	require.Fail(t, "CSV output variant not found")
	return inventory.ServerTool{}
}

func testCSVOutputRequest() *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: json.RawMessage(`{}`),
		},
	}
}

func readCSVResult(t *testing.T, result *mcp.CallToolResult) [][]string {
	t.Helper()

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)

	reader := csv.NewReader(strings.NewReader(text.Text))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	return records
}

func csvRow(t *testing.T, headers []string, record []string) map[string]string {
	t.Helper()
	require.Len(t, record, len(headers))

	row := make(map[string]string, len(headers))
	for i, header := range headers {
		row[header] = record[i]
	}
	return row
}
