package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"text/template"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The main function is required by the Go compiler, but it's not used by the plugin itself.
func main() {}

// getProtocolPrefix returns the corresponding protocol prefix based on the port number.
func getProtocolPrefix(port int64) string {
	if port == 80 {
		return "http://"
	}
	return "https://"
}

// The init function is used to initialize the plugin, setting up the context and request handlers.
func init() {
	wrapper.SetCtx(
		"ai-tool-selection",
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
	)
}

// ModelServiceConfig holds the configuration for calling an external model service.
type ModelServiceConfig struct {
	Client             wrapper.HttpClient
	ModelName          string
	TimeoutMillisecond int
	ApiKey             string
	ServiceDomain      string
	ServicePath        string
	ServicePort        int64
}

// ToolRerankingConfig defines the configuration for the tool reranking feature.
type ToolRerankingConfig struct {
	ModelService     ModelServiceConfig
	Protocol         string
	FilteringMethod  string
	TopKPercent      int
	TopNCount        int
	ScoreThreshold   float64
	FallbackStrategy string
}

// EnableConditionsConfig holds the conditions for enabling the overall feature.
type EnableConditionsConfig struct {
	ToolCountThreshold int // Tool count threshold. If the number of tools is less than this value, all processing is skipped. Default is 10.
}

// TriggerConditionsConfig holds the logic for triggering query rewriting.
type TriggerConditionsConfig struct {
	MessageCountThreshold int // When the number of dialogue rounds exceeds this threshold, query rewriting is triggered. 0 or a negative number disables it.
}

// ContextSelectionConfig defines how to select context from messages for query rewriting.
type ContextSelectionConfig struct {
	Type  string
	Value int
}

// QueryRewritingConfig defines the configuration for the query rewriting feature.
type QueryRewritingConfig struct {
	Enabled           bool
	ModelService      ModelServiceConfig
	PromptTemplate    string
	MaxOutputTokens   int
	TriggerConditions TriggerConditionsConfig
	ContextSelection  ContextSelectionConfig
	FallbackStrategy  string
}

// PluginConfig is the top-level configuration structure for the AI tool selection plugin.
type PluginConfig struct {
	Enabled          bool
	EnableConditions EnableConditionsConfig
	ToolReranking    ToolRerankingConfig
	QueryRewriting   QueryRewritingConfig
}

// RerankResult represents a single result item in the reranking model's response.
type RerankResult struct {
	Index float64 `json:"index"`
	Score float64 `json:"score"`
}

// TemplateData defines the data structure used for template rendering.
type TemplateData struct {
	Context         string
	MaxOutputTokens int
}

// parseConfig deserializes the JSON configuration from the control plane into the PluginConfig struct.
func parseConfig(json gjson.Result, config *PluginConfig) error {
	log.Infof("Parsing config: %v", json)
	// Parse the global 'enabled' flag. If false, no need to parse the rest.
	config.Enabled = json.Get("enabled").Bool()
	if !config.Enabled {
		return nil
	}

	// === Parse enable conditions config ===
	enableConditionsJSON := json.Get("enableConditions")
	if enableConditionsJSON.Exists() {
		config.EnableConditions.ToolCountThreshold = int(enableConditionsJSON.Get("toolCountThreshold").Int())
	}
	// If not configured or configured as 0, set the default value to 10.
	if config.EnableConditions.ToolCountThreshold <= 0 {
		config.EnableConditions.ToolCountThreshold = 10
	}

	// === Parse tool reranking config ===
	rerankJSON := json.Get("toolReranking")
	if !rerankJSON.Exists() {
		return errors.New("missing required config: toolReranking")
	}
	config.ToolReranking.FilteringMethod = rerankJSON.Get("filteringMethod").String()
	config.ToolReranking.TopKPercent = int(rerankJSON.Get("topKPercent").Int())
	config.ToolReranking.TopNCount = int(rerankJSON.Get("topNCount").Int())
	config.ToolReranking.ScoreThreshold = rerankJSON.Get("scoreThreshold").Float()
	config.ToolReranking.FallbackStrategy = rerankJSON.Get("fallbackStrategy").String()
	config.ToolReranking.ModelService.ModelName = rerankJSON.Get("modelName").String()
	config.ToolReranking.ModelService.TimeoutMillisecond = int(rerankJSON.Get("timeoutMillisecond").Int())
	config.ToolReranking.ModelService.ApiKey = rerankJSON.Get("apiKey").String()
	config.ToolReranking.ModelService.ServiceDomain = rerankJSON.Get("serviceDomain").String()
	config.ToolReranking.ModelService.ServicePath = rerankJSON.Get("servicePath").String()
	config.ToolReranking.ModelService.ServicePort = rerankJSON.Get("servicePort").Int()

	// Parse protocol config, default is dashscope.
	protocol := rerankJSON.Get("protocol").String()
	if protocol == "" {
		protocol = "dashscope"
	}
	if protocol != "dashscope" && protocol != "vllm" {
		return errors.New("protocol must be 'dashscope' or 'vllm'")
	}
	config.ToolReranking.Protocol = protocol

	// Create HTTP client for the reranking service.
	rerankServiceName := rerankJSON.Get("serviceName").String()
	rerankServicePort := rerankJSON.Get("servicePort").Int()
	if rerankServiceName == "" || rerankServicePort == 0 {
		return errors.New("invalid service config for toolReranking")
	}
	config.ToolReranking.ModelService.Client = wrapper.NewClusterClient(wrapper.FQDNCluster{
		FQDN: rerankServiceName,
		Port: rerankServicePort,
	})

	// === Parse query rewriting config (if it exists and is enabled) ===
	rewriteJSON := json.Get("queryRewriting")
	config.QueryRewriting.Enabled = rewriteJSON.Get("enabled").Bool()
	if config.QueryRewriting.Enabled {
		config.QueryRewriting.PromptTemplate = rewriteJSON.Get("promptTemplate").String()
		config.QueryRewriting.MaxOutputTokens = int(rewriteJSON.Get("maxOutputTokens").Int())
		config.QueryRewriting.FallbackStrategy = rewriteJSON.Get("fallbackStrategy").String()
		config.QueryRewriting.ModelService.ModelName = rewriteJSON.Get("modelName").String()
		config.QueryRewriting.ModelService.TimeoutMillisecond = int(rewriteJSON.Get("timeoutMillisecond").Int())
		config.QueryRewriting.ModelService.ApiKey = rewriteJSON.Get("apiKey").String()
		config.QueryRewriting.ModelService.ServiceDomain = rewriteJSON.Get("serviceDomain").String()
		config.QueryRewriting.ModelService.ServicePath = rewriteJSON.Get("servicePath").String()
		config.QueryRewriting.ModelService.ServicePort = rewriteJSON.Get("servicePort").Int()

		// Create HTTP client for the rewriting service.
		rewriteServiceName := rewriteJSON.Get("serviceName").String()
		rewriteServicePort := rewriteJSON.Get("servicePort").Int()
		if rewriteServiceName == "" || rewriteServicePort == 0 {
			return errors.New("invalid service config for queryRewriting")
		}
		config.QueryRewriting.ModelService.Client = wrapper.NewClusterClient(wrapper.FQDNCluster{
			FQDN: rewriteServiceName,
			Port: rewriteServicePort,
		})

		triggerJSON := rewriteJSON.Get("triggerConditions")
		config.QueryRewriting.TriggerConditions.MessageCountThreshold = int(triggerJSON.Get("messageCountThreshold").Int())

		// Parse context selection
		contextJSON := rewriteJSON.Get("contextSelection")
		config.QueryRewriting.ContextSelection.Type = contextJSON.Get("type").String()
		config.QueryRewriting.ContextSelection.Value = int(contextJSON.Get("value").Int())
	}

	return nil
}

// onHttpRequestHeaders handles the request headers phase.
// Returns HeaderStopIteration to pause header processing and allow body processing to modify headers.
func onHttpRequestHeaders(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	// Check if the feature is globally disabled
	if !config.Enabled {
		return types.ActionContinue
	}

	// Return HeaderStopIteration to pause the request headers processing
	// This allows us to modify headers in the body processing phase
	return types.HeaderStopIteration
}

// onHttpRequestBody is the main entry point for processing requests.
func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte) types.Action {
	log.Debug("Entering onHttpRequestBody processing.")
	// 1. Check if the feature is globally disabled.
	if !config.Enabled {
		log.Debug("Plugin is globally disabled, skipping processing.")
		return types.ActionContinue
	}
	log.Debugf("Request body size: %d bytes", len(body))

	// Parse the incoming request body to extract tools and messages.
	requestBodyJSON := gjson.ParseBytes(body)
	tools := requestBodyJSON.Get("tools").Array()
	messages := requestBodyJSON.Get("messages").Array()
	log.Debugf("Parsed %d tools and %d messages from request body.", len(tools), len(messages))

	// If there are no tools, there's nothing to filter, so continue.
	if len(tools) == 0 {
		log.Debug("No tools in the request, skipping processing.")
		return types.ActionContinue
	}

	// Check if the number of tools meets the enable condition.
	if len(tools) < config.EnableConditions.ToolCountThreshold {
		log.Debugf("Tool count (%d) is less than the configured threshold (%d), skipping all rewrite and rerank processes.", len(tools), config.EnableConditions.ToolCountThreshold)
		return types.ActionContinue
	}

	// Store the original request body in the context for fallback or later reconstruction.
	ctx.SetContext("originalBody", body)
	ctx.SetContext("originalTools", tools)

	// 2. Query rewriting stage
	// Check if query rewriting is enabled and if the trigger conditions are met.
	if config.QueryRewriting.Enabled && shouldTriggerRewrite(requestBodyJSON, config.QueryRewriting.TriggerConditions) {
		log.Info("Query rewriting has been triggered.")
		// Start the asynchronous query rewriting process.
		return startQueryRewriting(ctx, config, messages)
	}

	// If no rewriting is done, proceed directly to the tool reranking stage with the original query.
	log.Info("Skipping query rewriting, proceeding directly to tool reranking.")
	originalQuery := extractOriginalQuery(messages)
	return startToolReranking(ctx, config, originalQuery, tools)
}

// extractOriginalQuery gets the content of the last message as the default query.
func extractOriginalQuery(messages []gjson.Result) string {
	if len(messages) > 0 {
		return messages[len(messages)-1].Get("content").String()
	}
	return ""
}

// shouldTriggerRewrite checks if the conditions for query rewriting are met.
func shouldTriggerRewrite(bodyJSON gjson.Result, triggers TriggerConditionsConfig) bool {
	// If the threshold is 0 or negative, this trigger condition is disabled.
	if triggers.MessageCountThreshold <= 0 {
		log.Debug("Query rewriting trigger threshold is 0 or less, considered disabled.")
		return false
	}

	messageCount := len(bodyJSON.Get("messages").Array())
	log.Debugf("Checking trigger conditions: current message count %d, threshold %d", messageCount, triggers.MessageCountThreshold)

	// Trigger only when the message count is strictly greater than the threshold.
	decision := messageCount > triggers.MessageCountThreshold
	log.Debugf("Trigger condition check result: %v", decision)
	return decision
}

// startQueryRewriting builds the context and calls the rewriting model.
func startQueryRewriting(ctx wrapper.HttpContext, config PluginConfig, messages []gjson.Result) types.Action {
	log.Debug("Starting query rewriting process...")
	// Build context from messages based on configuration.
	contextStr := buildRewriteContext(messages, config.QueryRewriting.ContextSelection)
	log.Debugf("Context built for query rewriting: \n---\n%s\n---", contextStr)

	// Extract the current query (last user message) from messages.
	currentQuery := extractOriginalQuery(messages)
	log.Debugf("Extracted current query: %s", currentQuery)

	// Prepare template data.
	templateData := TemplateData{
		Context:         contextStr,
		MaxOutputTokens: config.QueryRewriting.MaxOutputTokens,
	}

	// Create a prompt using template rendering.
	prompt, err := renderPromptTemplate(config.QueryRewriting.PromptTemplate, templateData)
	if err != nil {
		log.Errorf("Failed to render query rewriting template: %v | Request details on failure: template content=%s, context length=%d, query content=%s", err, config.QueryRewriting.PromptTemplate, len(contextStr), currentQuery)
		return handleRewriteFailure(ctx, config)
	}
	log.Debugf("Final generated query rewriting prompt: \n---\n%s\n---", prompt)

	// Build the request payload for the rewriting model.
	payloadMap := map[string]interface{}{
		"model": config.QueryRewriting.ModelService.ModelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": config.QueryRewriting.MaxOutputTokens,
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		log.Errorf("Failed to serialize rewrite request payload: %v | Request details on failure: model name=%s, max output tokens=%d, prompt template=%s, context=%s, current query=%s, final prompt=%s, original payload struct=%+v", err, config.QueryRewriting.ModelService.ModelName, config.QueryRewriting.MaxOutputTokens, config.QueryRewriting.PromptTemplate, contextStr, currentQuery, prompt, payloadMap)
		return handleRewriteFailure(ctx, config)
	}
	log.Debugf("Request body sent to query rewriting service: %s", string(payload))

	// Make an asynchronous HTTP call to the rewriting service.
	client := config.QueryRewriting.ModelService.Client
	timeout := uint32(config.QueryRewriting.ModelService.TimeoutMillisecond)

	// Build request headers, adding Authorization header if apiKey is present.
	headers := [][2]string{{"Content-Type", "application/json"}}
	if config.QueryRewriting.ModelService.ApiKey != "" {
		headers = append(headers, [2]string{"Authorization", "Bearer " + config.QueryRewriting.ModelService.ApiKey})
	}

	// Build the full request URL, determining the protocol prefix from the port.
	protocolPrefix := getProtocolPrefix(config.QueryRewriting.ModelService.ServicePort)
	requestURL := protocolPrefix + config.QueryRewriting.ModelService.ServiceDomain + config.QueryRewriting.ModelService.ServicePath

	err = client.Post(requestURL, headers, payload,
		func(statusCode int, responseHeaders http.Header, responseBody []byte) {
			// Callback function to handle the response from the rewriting service.
			log.Debugf("Received response from query rewriting service: statusCode=%d, body=%s", statusCode, string(responseBody))
			if statusCode != 200 {
				log.Errorf("Query rewriting service returned non-200 status code: %d | Request details on failure: response status code=%d, response headers=%v, response body=%s, request body=%s, model name=%s, timeout=%dms", statusCode, statusCode, responseHeaders, string(responseBody), string(payload), config.QueryRewriting.ModelService.ModelName, config.QueryRewriting.ModelService.TimeoutMillisecond)
				handleRewriteFailure(ctx, config)
				return
			}

			// Extract the rewritten query from the response.
			rewrittenQuery := gjson.GetBytes(responseBody, "choices.0.message.content").String()
			if rewrittenQuery == "" {
				log.Errorf("Rewrite service response is empty or has wrong format | Request details on failure: response status code=%d, response body=%s, request body=%s, model name=%s, extraction path=choices.0.message.content", statusCode, string(responseBody), string(payload), config.QueryRewriting.ModelService.ModelName)
				handleRewriteFailure(ctx, config)
				return
			}

			log.Infof("Query successfully rewritten to: %s", rewrittenQuery)

			// Use the rewritten query and proceed to the tool reranking stage.
			originalTools := ctx.GetContext("originalTools").([]gjson.Result)
			startToolReranking(ctx, config, rewrittenQuery, originalTools)
		}, timeout)

	if err != nil {
		log.Errorf("Failed to dispatch HTTP call to rewrite service: %v | Request details on failure: service name=%v, request path=/, request headers=Content-Type:application/json, request body=%s, timeout=%dms, model name=%s, max output tokens=%d, prompt template=%s, context=%s, current query=%s", err, config.QueryRewriting.ModelService.Client, string(payload), config.QueryRewriting.ModelService.TimeoutMillisecond, config.QueryRewriting.ModelService.ModelName, config.QueryRewriting.MaxOutputTokens, config.QueryRewriting.PromptTemplate, contextStr, currentQuery)
		return handleRewriteFailure(ctx, config)
	}

	// Pause the original request to wait for the rewrite callback.
	log.Debug("Pausing HTTP request, waiting for query rewriting to complete.")
	return types.ActionPause
}

// buildRewriteContext builds a string context from messages based on the selection strategy.
func buildRewriteContext(messages []gjson.Result, selection ContextSelectionConfig) string {
	var contextBuilder strings.Builder
	var selectedMessages []gjson.Result

	switch selection.Type {
	case "allMessages":
		selectedMessages = messages
	case "recentMessages":
		startIndex := len(messages) - selection.Value
		if startIndex < 0 {
			startIndex = 0
		}
		selectedMessages = messages[startIndex:]
	default: // Default to the last message
		if len(messages) > 0 {
			selectedMessages = messages[len(messages)-1:]
		}
	}

	for _, msg := range selectedMessages {
		role := msg.Get("role").String()
		content := msg.Get("content").String()
		fmt.Fprintf(&contextBuilder, "%s: %s\n", role, content)
	}

	return contextBuilder.String()
}

// renderPromptTemplate renders a template string with the provided data.
func renderPromptTemplate(templateStr string, data TemplateData) (string, error) {
	tmpl, err := template.New("prompt").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	templateData := map[string]interface{}{
		"Context":         data.Context,
		"MaxOutputTokens": data.MaxOutputTokens,
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, templateData)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// handleRewriteFailure decides what to do when the rewrite service fails, based on the fallback strategy.
func handleRewriteFailure(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	log.Warn("Entering query rewrite failure handling process...")
	if config.QueryRewriting.FallbackStrategy == "error" {
		log.Error("Query rewriting failed, configured to interrupt the request.")
		proxywasm.SendHttpResponse(503, nil, []byte("Query rewriting service failed."), -1)
		return types.ActionPause
	}

	// Default strategy: "skip"
	log.Warn("Query rewriting failed, skipping and using original query for reranking.")
	messages := gjson.ParseBytes(ctx.GetContext("originalBody").([]byte)).Get("messages").Array()
	originalQuery := extractOriginalQuery(messages)
	originalTools := ctx.GetContext("originalTools").([]gjson.Result)
	return startToolReranking(ctx, config, originalQuery, originalTools)
}

// startToolReranking prepares and sends a request to the reranking model.
func startToolReranking(ctx wrapper.HttpContext, config PluginConfig, query string, tools []gjson.Result) types.Action {
	log.Debugf("Starting tool reranking process, query: '%s'", query)
	// Prepare documents for reranking (using tool names and descriptions).
	var documents []string
	for _, tool := range tools {
		// Try to get function.name and function.description.
		functionName := tool.Get("function.name").String()
		functionDescription := tool.Get("function.description").String()

		// If function.name or function.description is empty, use name and description as fallbacks.
		if functionName == "" {
			functionName = tool.Get("name").String()
		}
		if functionDescription == "" {
			functionDescription = tool.Get("description").String()
		}

		// Build the document in the format 'function.name: function.description'.
		document := functionName + ": " + functionDescription
		documents = append(documents, document)
	}

	// Build the payload for the reranking model, constructing different formats based on the protocol type.
	var payloadMap map[string]interface{}
	switch config.ToolReranking.Protocol {
	case "dashscope":
		payloadMap = map[string]interface{}{
			"model": config.ToolReranking.ModelService.ModelName,
			"input": map[string]interface{}{
				"query":     query,
				"documents": documents,
			},
			"parameters": map[string]interface{}{
				"return_documents": true,
				"top_n":            len(documents), // Return all documents, filtering will be done later.
			},
		}
	case "vllm":
		payloadMap = map[string]interface{}{
			"query":     query,
			"documents": documents,
		}
	default:
		log.Errorf("Unsupported protocol: %s", config.ToolReranking.Protocol)
		return handleRerankFailure(ctx, config)
	}

	payload, err := json.Marshal(payloadMap)
	if err != nil {
		log.Errorf("Failed to serialize rerank request payload: %v | Request details on failure: protocol type=%s, model name=%s, query content=%s, tool count=%d, tool descriptions=%v, original payload struct=%+v", err, config.ToolReranking.Protocol, config.ToolReranking.ModelService.ModelName, query, len(tools), documents, payloadMap)
		return handleRerankFailure(ctx, config)
	}
	log.Debugf("Request body sent to tool reranking service: %s", string(payload))

	// Make an asynchronous HTTP call to the reranking service.
	client := config.ToolReranking.ModelService.Client
	timeout := uint32(config.ToolReranking.ModelService.TimeoutMillisecond)

	// Build request headers, adding Authorization header if apiKey is present.
	headers := [][2]string{{"Content-Type", "application/json"}}
	if config.ToolReranking.ModelService.ApiKey != "" {
		headers = append(headers, [2]string{"Authorization", "Bearer " + config.ToolReranking.ModelService.ApiKey})
	}

	// Build the full request URL, determining the protocol prefix from the port.
	protocolPrefix := getProtocolPrefix(config.ToolReranking.ModelService.ServicePort)
	requestURL := protocolPrefix + config.ToolReranking.ModelService.ServiceDomain + config.ToolReranking.ModelService.ServicePath

	err = client.Post(requestURL, headers, payload,
		func(statusCode int, responseHeaders http.Header, responseBody []byte) {
			// Callback to handle the response from the reranking service.
			log.Debugf("Received response from tool reranking service: statusCode=%d, body=%s", statusCode, string(responseBody))
			if statusCode != 200 {
				log.Errorf("Reranking service returned non-200 status code: %d | Request details on failure: response status code=%d, response headers=%v, response body=%s, request URL=%s, request body=%s, model name=%s, timeout=%dms, query content=%s, tool count=%d", statusCode, statusCode, responseHeaders, string(responseBody), requestURL, string(payload), config.ToolReranking.ModelService.ModelName, config.ToolReranking.ModelService.TimeoutMillisecond, query, len(tools))
				handleRerankFailure(ctx, config)
				return
			}

			// Parse the reranking results, handling different response formats based on the protocol type.
			var rerankResults []RerankResult

			switch config.ToolReranking.Protocol {
			case "dashscope":
				// Handle dashscope format response.
				outputResults := gjson.GetBytes(responseBody, "output.results").Array()
				for _, result := range outputResults {
					rerankResults = append(rerankResults, RerankResult{
						Index: result.Get("index").Float(),
						Score: result.Get("relevance_score").Float(),
					})
				}
			case "vllm":
				// Handle vllm format response.
				results := gjson.GetBytes(responseBody, "results").Array()
				for _, result := range results {
					rerankResults = append(rerankResults, RerankResult{
						Index: result.Get("index").Float(),
						Score: result.Get("relevance_score").Float(),
					})
				}
			default:
				log.Errorf("Unsupported protocol: %s", config.ToolReranking.Protocol)
				handleRerankFailure(ctx, config)
				return
			}

			log.Debugf("Successfully parsed %d reranking results.", len(rerankResults))

			// Filter and sort the original tools based on the reranking scores.
			selectedTools := filterAndSortTools(rerankResults, tools, config.ToolReranking)

			// Reconstruct the request body with the new, filtered list of tools.
			originalBody := ctx.GetContext("originalBody").([]byte)
			var newBody []byte
			var err error
			if len(selectedTools) == 0 {
				log.Info("Tool list is empty, will remove 'tools' parameter from request.")
				newBody, err = sjson.DeleteBytes(originalBody, "tools")
			} else {
				newBody, err = sjson.SetBytes(originalBody, "tools", selectedTools)
			}
			if err != nil {
				log.Errorf("Failed to replace tool list in request body: %v | Request details on failure: original body size=%d bytes, selected tool count=%d, original tool count=%d, query content=%s, rerank result count=%d, filtering method=%s", err, len(originalBody), len(selectedTools), len(tools), query, len(rerankResults), config.ToolReranking.FilteringMethod)
				handleRerankFailure(ctx, config) // Fallback if reconstruction fails.
				return
			}

			// Replace the original request body and continue the request flow.
			log.Debugf("Replaced request body size: %d", len(newBody))
			proxywasm.ReplaceHttpRequestBody(newBody)
			// Remove content-length header since we modified the body
			proxywasm.RemoveHttpRequestHeader("content-length")
			log.Infof("Tool reranking successful. Selected %d tools out of %d.", len(selectedTools), len(tools))
			proxywasm.ResumeHttpRequest()
		}, timeout)

	if err != nil {
		log.Errorf("Failed to dispatch HTTP call to reranking service: %v | Request details on failure: service name=%v, request URL=%s, request headers=Content-Type:application/json, request body=%s, protocol type=%s, model name=%s, timeout=%dms, query content=%s, tool count=%d, filtering method=%s, score threshold=%.2f, TopK percent=%d%%, TopN count=%d", err, config.ToolReranking.ModelService.Client, requestURL, string(payload), config.ToolReranking.Protocol, config.ToolReranking.ModelService.ModelName, config.ToolReranking.ModelService.TimeoutMillisecond, query, len(tools), config.ToolReranking.FilteringMethod, config.ToolReranking.ScoreThreshold, config.ToolReranking.TopKPercent, config.ToolReranking.TopNCount)
		return handleRerankFailure(ctx, config)
	}

	// Pause the request to wait for the reranking results.
	log.Debug("Pausing HTTP request, waiting for tool reranking to complete.")
	return types.ActionPause
}

// handleRerankFailure decides what to do when the reranking service fails.
func handleRerankFailure(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	log.Warn("Entering tool rerank failure handling process...")
	if config.ToolReranking.FallbackStrategy == "error" {
		log.Error("Tool reranking failed, configured to interrupt the request.")
		proxywasm.SendHttpResponse(503, nil, []byte("Tool reranking service failed."), -1)
		return types.ActionPause
	}

	// Default strategy: "skip". Use the original request body and continue.
	log.Warn("Tool reranking failed, skipping and using original tool list.")
	originalBody := ctx.GetContext("originalBody").([]byte)
	proxywasm.ReplaceHttpRequestBody(originalBody)
	// Remove content-length header since we might have modified the body
	proxywasm.RemoveHttpRequestHeader("content-length")
	proxywasm.ResumeHttpRequest()
	return types.ActionPause // This pause will be lifted since ResumeHttpRequest has been called.
}

// filterAndSortTools applies the configured filtering logic to the reranked tools.
func filterAndSortTools(results []RerankResult, originalTools []gjson.Result, config ToolRerankingConfig) []interface{} {
	log.Debugf("Starting to filter tools, initial result count: %d", len(results))

	// 1. Filter by score threshold.
	var scoredTools []RerankResult
	for _, res := range results {
		if res.Score >= config.ScoreThreshold {
			scoredTools = append(scoredTools, res)
		}
	}
	log.Debugf("After applying score threshold %.2f, remaining tool count: %d", config.ScoreThreshold, len(scoredTools))

	// 2. Sort in descending order of score.
	sort.Slice(scoredTools, func(i, j int) bool {
		return scoredTools[i].Score > scoredTools[j].Score
	})
	log.Debug("Tools have been sorted by score in descending order.")

	// 3. Apply filtering method (Top-K, Top-N, Combined).
	limit := len(scoredTools)
	log.Debugf("Current tool limit: %d, filtering method: %s", limit, config.FilteringMethod)

	switch config.FilteringMethod {
	case "topK":
		kLimit := int(math.Ceil(float64(len(originalTools)) * (float64(config.TopKPercent) / 100.0)))
		log.Debugf("  - TopK(%.d%%) calculated limit: %d", config.TopKPercent, kLimit)
		if kLimit < limit {
			limit = kLimit
		}
	case "topN":
		nLimit := config.TopNCount
		log.Debugf("  - TopN limit: %d", nLimit)
		if nLimit < limit {
			limit = nLimit
		}
	case "combined":
		kLimit := int(math.Ceil(float64(len(originalTools)) * (float64(config.TopKPercent) / 100.0)))
		nLimit := config.TopNCount
		log.Debugf("  - Combined mode: TopK(%.d%%) limit=%d, TopN limit=%d", config.TopKPercent, kLimit, nLimit)
		finalLimit := kLimit
		if nLimit < finalLimit {
			finalLimit = nLimit
		}
		log.Debugf("  - Combined mode takes the smaller one: %d", finalLimit)
		if finalLimit < limit {
			limit = finalLimit
		}
	}

	// Ensure the limit does not exceed bounds.
	if limit > len(scoredTools) {
		limit = len(scoredTools)
	}
	log.Debugf("After applying filtering method, final tool limit: %d", limit)

	// 4. Build the final list of filtered tools.
	finalTools := make([]interface{}, 0, limit)
	for i := 0; i < limit; i++ {
		originalIndex := int(scoredTools[i].Index)
		if originalIndex >= 0 && originalIndex < len(originalTools) {
			finalTools = append(finalTools, originalTools[originalIndex].Value())
		} else {
			log.Warnf("Index %d from reranking result is out of original tool list bounds, ignored.", originalIndex)
		}
	}
	log.Debugf("Finally built %d tools.", len(finalTools))

	return finalTools
}
