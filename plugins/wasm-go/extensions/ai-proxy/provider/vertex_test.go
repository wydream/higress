package provider

import (
	"encoding/json"
	"testing"

	"github.com/alibaba/higress/plugins/wasm-go/extensions/ai-proxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendOrReplaceAPIKey(t *testing.T) {
	t.Run("empty apiKey returns path unchanged", func(t *testing.T) {
		path := "/v1/publishers/google/models/gemini:generateContent"
		assert.Equal(t, path, appendOrReplaceAPIKey(path, ""))
	})

	t.Run("path without query appends ?key=", func(t *testing.T) {
		result := appendOrReplaceAPIKey("/v1/models/gemini:generateContent", "my-key")
		assert.Equal(t, "/v1/models/gemini:generateContent?key=my-key", result)
	})

	t.Run("path with existing query appends &key=", func(t *testing.T) {
		result := appendOrReplaceAPIKey("/v1/models/gemini:streamGenerateContent?alt=sse", "my-key")
		assert.Contains(t, result, "alt=sse")
		assert.Contains(t, result, "key=my-key")
	})

	t.Run("existing key parameter is replaced", func(t *testing.T) {
		result := appendOrReplaceAPIKey("/v1/models/gemini:generateContent?key=old-key&trace=1", "new-key")
		assert.Contains(t, result, "key=new-key")
		assert.NotContains(t, result, "old-key")
		assert.Contains(t, result, "trace=1")
	})

	t.Run("unparseable path without query falls back to ?key= append", func(t *testing.T) {
		// A bare string with no leading slash is not a valid RequestURI
		result := appendOrReplaceAPIKey("not-a-valid-uri", "my-key")
		assert.Equal(t, "not-a-valid-uri?key=my-key", result)
	})

	t.Run("unparseable path with query falls back to &key= append", func(t *testing.T) {
		result := appendOrReplaceAPIKey("not-a-valid-uri?foo=bar", "my-key")
		assert.Equal(t, "not-a-valid-uri?foo=bar&key=my-key", result)
	})
}

func TestBuildVertexFunctionDeclarations(t *testing.T) {
	schema := map[string]interface{}{
		"type":    "object",
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"properties": map[string]interface{}{
			"city": map[string]interface{}{
				"type":        "string",
				"description": "city name",
			},
		},
		"required": []interface{}{"city"},
	}

	declarations := buildVertexFunctionDeclarations([]tool{
		{
			Type: "function",
			Function: function{
				Name:        "demo_tool",
				Description: "demo",
				Parameters:  schema,
			},
		},
		{
			Type: "function",
			Function: function{
				Name: "no_params",
			},
		},
	})

	require.Len(t, declarations, 2)
	assert.Equal(t, "demo_tool", declarations[0].Name)
	assert.Equal(t, "demo", declarations[0].Description)
	assert.Equal(t, schema, declarations[0].ParametersJsonSchema)
	assert.Equal(t, "no_params", declarations[1].Name)
	assert.Empty(t, declarations[1].Description)
	assert.Nil(t, declarations[1].ParametersJsonSchema)

	body, err := json.Marshal(vertexTool{FunctionDeclarations: declarations[:1]})
	require.NoError(t, err)
	bodyString := string(body)
	assert.Contains(t, bodyString, `"parametersJsonSchema"`)
	assert.Contains(t, bodyString, `"$schema"`)
	assert.NotContains(t, bodyString, `"parameters":`)
}

func TestVertexProviderBuildChatRequestToolParametersUseParametersJsonSchema(t *testing.T) {
	v := &vertexProvider{}
	schema := map[string]interface{}{
		"type":    "object",
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"properties": map[string]interface{}{
			"city": map[string]interface{}{
				"type":        "string",
				"description": "city name",
			},
		},
		"required": []interface{}{"city"},
	}
	req := &chatCompletionRequest{
		Model: "gemini-2.5-flash",
		Messages: []chatMessage{
			{Role: roleUser, Content: "test tools"},
		},
		Tools: []tool{
			{
				Type: "function",
				Function: function{
					Name:        "demo_tool",
					Description: "demo",
					Parameters:  schema,
				},
			},
		},
	}

	vertexReq, err := v.buildVertexChatRequest(req)
	require.NoError(t, err)
	require.NotNil(t, vertexReq)
	require.Len(t, vertexReq.Tools, 1)
	require.Len(t, vertexReq.Tools[0].FunctionDeclarations, 1)
	declaration := vertexReq.Tools[0].FunctionDeclarations[0]
	assert.Equal(t, "demo_tool", declaration.Name)
	assert.Equal(t, "demo", declaration.Description)
	assert.Equal(t, schema, declaration.ParametersJsonSchema)

	body, err := json.Marshal(vertexReq)
	require.NoError(t, err)
	bodyString := string(body)
	assert.Contains(t, bodyString, `"functionDeclarations"`)
	assert.Contains(t, bodyString, `"parametersJsonSchema"`)
	assert.Contains(t, bodyString, `"$schema"`)
	assert.NotContains(t, bodyString, `"parameters":`)
}

func TestVertexProviderBuildChatRequestStructuredOutputMapping(t *testing.T) {
	t.Run("json_object response format", func(t *testing.T) {
		v := &vertexProvider{}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type": "json_object",
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)

		assert.Equal(t, util.MimeTypeApplicationJson, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Nil(t, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("json_schema response format with nested schema", func(t *testing.T) {
		v := &vertexProvider{}
		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"answer": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []interface{}{"answer"},
		}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type": "json_schema",
				"json_schema": map[string]interface{}{
					"name":   "response",
					"strict": true,
					"schema": schema,
				},
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)

		assert.Equal(t, util.MimeTypeApplicationJson, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Equal(t, schema, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("json_schema response format with direct schema object", func(t *testing.T) {
		v := &vertexProvider{}
		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"city": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []interface{}{"city"},
		}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type":        "json_schema",
				"json_schema": schema,
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)

		assert.Equal(t, util.MimeTypeApplicationJson, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Equal(t, schema, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("json_schema response format without valid schema should return error", func(t *testing.T) {
		v := &vertexProvider{}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type":        "json_schema",
				"json_schema": "invalid",
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.Error(t, err)
		assert.Nil(t, vertexReq)
		assert.Contains(t, err.Error(), "invalid response_format.json_schema")
	})

	t.Run("direct schema in response_format for compatibility", func(t *testing.T) {
		v := &vertexProvider{}
		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"result": map[string]interface{}{
					"type": "string",
				},
			},
		}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: schema,
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)

		assert.Equal(t, util.MimeTypeApplicationJson, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Equal(t, schema, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("text response format keeps default text output", func(t *testing.T) {
		v := &vertexProvider{}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type": "text",
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)

		assert.Empty(t, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Nil(t, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("unknown response format does not inject schema config", func(t *testing.T) {
		v := &vertexProvider{}
		req := &chatCompletionRequest{
			Model: "gemini-2.5-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type": "xml",
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)

		assert.Empty(t, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Nil(t, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("gemini 2.0 json_schema is ignored for stability", func(t *testing.T) {
		v := &vertexProvider{}
		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"answer": map[string]interface{}{
					"type": "string",
				},
			},
		}
		req := &chatCompletionRequest{
			Model: "gemini-2.0-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type": "json_schema",
				"json_schema": map[string]interface{}{
					"name":   "response",
					"strict": true,
					"schema": schema,
				},
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)
		assert.Empty(t, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Nil(t, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("gemini 2.0 malformed json_schema is also ignored", func(t *testing.T) {
		v := &vertexProvider{}
		req := &chatCompletionRequest{
			Model: "gemini-2.0-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type":        "json_schema",
				"json_schema": "invalid",
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)
		assert.Empty(t, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Nil(t, vertexReq.GenerationConfig.ResponseSchema)
	})

	t.Run("gemini 2.0 json_object is ignored", func(t *testing.T) {
		v := &vertexProvider{}
		req := &chatCompletionRequest{
			Model: "gemini-2.0-flash",
			Messages: []chatMessage{
				{Role: roleUser, Content: "hello"},
			},
			ResponseFormat: map[string]interface{}{
				"type": "json_object",
			},
		}

		vertexReq, err := v.buildVertexChatRequest(req)
		require.NoError(t, err)
		require.NotNil(t, vertexReq)
		assert.Empty(t, vertexReq.GenerationConfig.ResponseMimeType)
		assert.Nil(t, vertexReq.GenerationConfig.ResponseSchema)
	})
}

func TestVertexProviderApplyResponseFormatNilSafety(t *testing.T) {
	v := &vertexProvider{}
	require.NoError(t, v.applyResponseFormatToGenerationConfig(map[string]interface{}{"type": "json_object"}, nil, "gemini-2.5-flash"))
	require.NoError(t, v.applyResponseFormatToGenerationConfig(nil, &vertexChatGenerationConfig{}, "gemini-2.5-flash"))
	require.NoError(t, v.applyResponseFormatToGenerationConfig(map[string]interface{}{}, &vertexChatGenerationConfig{}, "gemini-2.5-flash"))
}
