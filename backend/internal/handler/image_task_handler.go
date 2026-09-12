package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type AsyncImageHandler struct {
	tasks   *service.ImageTaskService
	openAI  *OpenAIGatewayHandler
	ops     *service.OpsService
	execute func(platform string, c *gin.Context)
}

func NewAsyncImageHandler(tasks *service.ImageTaskService, openAI *OpenAIGatewayHandler, ops ...*service.OpsService) *AsyncImageHandler {
	var opsService *service.OpsService
	if len(ops) > 0 {
		opsService = ops[0]
	}
	h := &AsyncImageHandler{tasks: tasks, openAI: openAI, ops: opsService}
	h.execute = h.executeWithGateway
	return h
}

// enabled reports whether the async image task feature is available. Object
// storage is the enablement gate: without it the endpoints are fully disabled
// so that large base64 results never land in Redis.
func (h *AsyncImageHandler) enabled() bool {
	return h != nil && h.tasks != nil && h.tasks.Enabled()
}

// pollable reports whether task lookups can be served. It is deliberately weaker
// than enabled(): results already written to Redis stay readable after the
// feature is switched off, so an in-flight task is never stranded.
func (h *AsyncImageHandler) pollable() bool {
	return h != nil && h.tasks != nil && h.tasks.Pollable()
}

// Submit accepts the same payload as the synchronous Images endpoint and
// returns before the upstream image generation begins.
func (h *AsyncImageHandler) Submit(c *gin.Context) {
	if !h.enabled() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	platform := ""
	if apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	if platform != service.PlatformOpenAI && platform != service.PlatformGrok {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "Images API is not supported for this platform")
		return
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		imageTaskJSONError(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	if h == nil || h.tasks == nil || h.execute == nil {
		imageTaskError(c, service.ErrImageTaskUnavailable)
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			imageTaskJSONError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	if asyncImageRequestStreams(c.GetHeader("Content-Type"), body) {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "streaming image requests cannot be submitted as asynchronous tasks")
		return
	}
	if err := h.validateRequest(c, platform, body); err != nil {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if !h.checkSecurityAuditBeforeSubmit(c, apiKey, platform, body) {
		return
	}

	taskCtx, recorder, cancel := newAsyncImageContext(c, body, h.tasks.ExecutionTimeout())
	task, err := h.tasks.Create(c.Request.Context(), service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID})
	if err != nil {
		cancel()
		imageTaskError(c, err)
		return
	}

	pollURL := imageTaskPollURL(c.Request.URL.Path, task.ID)
	c.Header("Cache-Control", "no-store")
	c.Header("Location", pollURL)
	c.Header("Retry-After", "3")
	c.JSON(http.StatusAccepted, gin.H{
		"id":         task.ID,
		"task_id":    task.TaskID,
		"object":     task.Object,
		"status":     task.Status,
		"created_at": task.CreatedAt,
		"expires_at": task.ExpiresAt,
		"poll_url":   pollURL,
	})

	go h.run(task.ID, platform, taskCtx, recorder, cancel)
}

func (h *AsyncImageHandler) checkSecurityAuditBeforeSubmit(c *gin.Context, apiKey *service.APIKey, platform string, body []byte) bool {
	if h == nil || h.openAI == nil {
		return true
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		imageTaskJSONError(c, http.StatusInternalServerError, "api_error", "User context not found")
		return false
	}
	model := ""
	moderationBody := body
	if platform == service.PlatformGrok {
		parsed := service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body)
		model, moderationBody = parsed.Model, parsed.ModerationBody()
	} else if h.openAI.gatewayService != nil {
		parsed, err := h.openAI.gatewayService.ParseOpenAIImagesRequest(c, body)
		if err != nil {
			imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return false
		}
		model, moderationBody = parsed.Model, parsed.ModerationBody()
	}
	if len(moderationBody) == 0 {
		c.Set(securityAuditCompletedContextKey, true)
		return true
	}
	reqLog := requestLogger(c, "handler.async_image.security_audit",
		zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", apiKey.ID), zap.String("model", model))
	decision := h.openAI.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIImages, model, moderationBody)
	if decision != nil && !decision.AllowNextStage {
		h.openAI.openAISecurityAuditError(c, decision)
		return false
	}
	return true
}

func (h *AsyncImageHandler) Get(c *gin.Context) {
	// Polling deliberately does not require the feature to be enabled, only that
	// the task store is reachable. Turning the switch off in the admin UI must not
	// strand tasks that were already accepted — their results are still in Redis
	// and their submitters are still polling.
	if !h.pollable() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	task, err := h.tasks.Get(c.Request.Context(), service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID}, c.Param("task_id"))
	if err != nil {
		imageTaskError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if task.Status == service.ImageTaskStatusProcessing {
		c.Header("Retry-After", "3")
	}
	c.JSON(http.StatusOK, task)
}

func (h *AsyncImageHandler) validateRequest(c *gin.Context, platform string, body []byte) error {
	if h.openAI == nil || h.openAI.gatewayService == nil {
		return nil
	}
	if platform == service.PlatformGrok {
		parsed := service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body)
		if strings.TrimSpace(parsed.Model) == "" {
			return errors.New("model is required")
		}
		return nil
	}
	parsed, err := h.openAI.gatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		return err
	}
	if parsed.Stream {
		return errors.New("streaming image requests cannot be submitted as asynchronous tasks")
	}
	return nil
}

func (h *AsyncImageHandler) executeWithGateway(platform string, c *gin.Context) {
	if h.openAI == nil {
		imageTaskJSONError(c, http.StatusServiceUnavailable, "api_error", "image gateway is unavailable")
		return
	}
	if platform == service.PlatformGrok {
		h.openAI.GrokImages(c)
		return
	}
	h.openAI.Images(c)
}

func (h *AsyncImageHandler) run(taskID, platform string, taskCtx *gin.Context, recorder *httptest.ResponseRecorder, cancel context.CancelFunc) {
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().Error("image_task.execution_panicked", zap.String("task_id", taskID), zap.Any("panic", recovered))
			h.failTask(taskID, platform, taskCtx, http.StatusInternalServerError, imageTaskErrorPayload("api_error", "image generation task panicked"))
		}
	}()

	h.execute(platform, taskCtx)
	body := bytes.TrimSpace(recorder.Body.Bytes())
	if err := taskCtx.Request.Context().Err(); err != nil && len(body) == 0 {
		h.failTask(taskID, platform, taskCtx, http.StatusGatewayTimeout, imageTaskErrorPayload("timeout_error", "image generation task timed out"))
		return
	}
	statusCode := recorder.Code
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		if len(body) == 0 || !json.Valid(body) {
			h.failTask(taskID, platform, taskCtx, http.StatusBadGateway, imageTaskErrorPayload("api_error", "upstream returned an invalid image response"))
			return
		}
		if err := h.tasks.Complete(context.Background(), taskID, statusCode, json.RawMessage(body)); err != nil {
			logger.L().Error("image_task.complete_store_failed", zap.String("task_id", taskID), zap.Error(err))
		}
		return
	}
	h.failTask(taskID, platform, taskCtx, statusCode, extractImageTaskError(body))
}

func (h *AsyncImageHandler) failTask(taskID, platform string, taskCtx *gin.Context, statusCode int, taskErr json.RawMessage) {
	if statusCode <= 0 {
		statusCode = http.StatusBadGateway
	}
	if err := h.tasks.Fail(context.Background(), taskID, statusCode, taskErr); err != nil {
		logger.L().Error("image_task.failure_store_failed", zap.String("task_id", taskID), zap.Error(err))
	}
	h.recordAsyncFailure(taskID, platform, taskCtx, statusCode, taskErr)
}

// recordAsyncFailure bridges the background task lifecycle into the normal Ops
// error stream. The submit middleware only observes the initial 202 response.
func (h *AsyncImageHandler) recordAsyncFailure(taskID, platform string, taskCtx *gin.Context, statusCode int, taskErr json.RawMessage) {
	if h == nil || h.ops == nil || taskCtx == nil {
		return
	}
	message, errorType := "image generation task failed", "api_error"
	var payload struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(taskErr, &payload) == nil {
		if strings.TrimSpace(payload.Message) != "" {
			message = strings.TrimSpace(payload.Message)
		}
		if strings.TrimSpace(payload.Type) != "" {
			errorType = strings.TrimSpace(payload.Type)
		} else if strings.TrimSpace(payload.Code) != "" {
			errorType = strings.TrimSpace(payload.Code)
		}
	}
	if statusCode == http.StatusTooManyRequests {
		errorType = "rate_limit_error"
	}
	switch errorType {
	case "invalid_request", "invalid_request_error":
		errorType = "invalid_request_error"
	case "service_unavailable", "service_unavailable_error", "server_error":
		errorType = "service_unavailable_error"
	}
	if statusCode >= 500 && errorType == "api_error" {
		errorType = "service_unavailable_error"
	}
	requestID, _ := taskCtx.Request.Context().Value(ctxkey.RequestID).(string)
	clientRequestID, _ := taskCtx.Request.Context().Value(ctxkey.ClientRequestID).(string)
	if strings.TrimSpace(requestID) == "" {
		requestID = taskID
	}
	apiKey := getOpsAPIKey(taskCtx)
	var userID, apiKeyID, groupID *int64
	if apiKey != nil {
		apiKeyID = &apiKey.ID
		if apiKey.UserID > 0 {
			v := apiKey.UserID
			userID = &v
		}
		if apiKey.GroupID != nil {
			groupID = apiKey.GroupID
		}
		if apiKey.Group != nil && apiKey.Group.Platform != "" {
			platform = apiKey.Group.Platform
		}
	}
	model := taskCtx.GetString(opsModelKey)
	if model == "" {
		model = taskCtx.GetString("model")
	}
	requestPath := ""
	if taskCtx.Request != nil && taskCtx.Request.URL != nil {
		requestPath = taskCtx.Request.URL.Path
	}
	if requestPath == "" {
		requestPath = "/images"
	}
	message += " [task_id=" + taskID + "]"
	if summary := asyncImageRequestSummary(taskCtx); summary != "" {
		message += " params=" + summary
	}
	phase, source, owner := "internal", "gateway", "platform"
	if errorType == "invalid_request_error" || statusCode == http.StatusBadRequest {
		phase, source, owner = "request", "client_request", "client"
	} else if statusCode == http.StatusUnauthorized {
		phase, source, owner = "auth", "gateway", "platform"
	} else if statusCode == http.StatusTooManyRequests || statusCode >= 500 {
		phase, source, owner = "upstream", "upstream_http", "provider"
	}
	entry := &service.OpsInsertErrorLogInput{
		RequestID: requestID, ClientRequestID: clientRequestID,
		UserID: userID, APIKeyID: apiKeyID, GroupID: groupID,
		Platform: resolveOpsPlatform(taskCtx.Request.Context(), apiKey, platform), Model: model,
		RequestPath: requestPath, InboundEndpoint: requestPath, Stream: false,
		ErrorPhase: phase, ErrorType: errorType, ErrorSource: source, ErrorOwner: owner,
		Severity: "error", StatusCode: statusCode, ErrorMessage: message,
		ErrorBody: string(taskErr), UserAgent: taskCtx.GetHeader("User-Agent"), CreatedAt: time.Now(),
	}
	if v, ok := taskCtx.Get(opsRequestTypeKey); ok {
		switch value := v.(type) {
		case int16:
			entry.RequestType = &value
		case int:
			converted := int16(value)
			entry.RequestType = &converted
		}
	}
	if v, ok := taskCtx.Get(opsAccountIDKey); ok {
		if value, ok := v.(int64); ok && value > 0 {
			entry.AccountID = &value
		}
	}
	if err := h.ops.RecordError(context.Background(), entry); err != nil {
		logger.L().Warn("image_task.ops_error_store_failed", zap.String("task_id", taskID), zap.Error(err))
	}
}

// asyncImageRequestSummary keeps only fields useful for diagnosing validation
// and upstream capability errors. Prompts, image data and credentials are not persisted.
func asyncImageRequestSummary(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.GetBody == nil {
		return ""
	}
	body, err := c.Request.GetBody()
	if err != nil {
		return ""
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			logger.L().Warn("image_task.request_summary_close_failed", zap.Error(closeErr))
		}
	}()
	data, readErr := io.ReadAll(io.LimitReader(body, 512<<10))
	if readErr != nil {
		return ""
	}
	return summarizeImageRequestBytes(c.GetHeader("Content-Type"), data)
}

func summarizeImageRequestBytes(contentType string, data []byte) string {
	keep := map[string]any{}
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		_, params, err := mime.ParseMediaType(contentType)
		boundary := params["boundary"]
		if err != nil || boundary == "" {
			return ""
		}
		reader := multipart.NewReader(bytes.NewReader(data), boundary)
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return ""
			}
			name := part.FormName()
			if part.FileName() != "" || !isAsyncImageSummaryField(name) {
				continue
			}
			value, readErr := io.ReadAll(io.LimitReader(part, 512))
			if readErr == nil {
				keep[name] = strings.TrimSpace(string(value))
			}
		}
	} else {
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			return ""
		}
		for _, key := range []string{"model", "size", "quality", "n", "response_format", "background", "moderation", "aspect_ratio", "aspectRatio"} {
			if value, ok := raw[key]; ok {
				keep[key] = value
			}
		}
	}
	if len(keep) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(keep)
	return string(encoded)
}

func captureImageRequestSummary(c *gin.Context) {
	if c == nil || c.Request == nil || c.Request.Body == nil || c.Request.URL == nil {
		return
	}
	path := strings.ToLower(c.Request.URL.Path)
	if !strings.Contains(path, "/images/") {
		return
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, 512<<10))
	// Restore the bytes consumed for inspection so the gateway sees the original body.
	c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), c.Request.Body))
	if err != nil {
		return
	}
	if summary := summarizeImageRequestBytes(c.GetHeader("Content-Type"), data); summary != "" {
		c.Set(opsImageRequestSummaryKey, summary)
	}
}

func isAsyncImageSummaryField(name string) bool {
	switch strings.TrimSpace(name) {
	case "model", "size", "quality", "n", "response_format", "background", "moderation", "aspect_ratio", "aspectRatio":
		return true
	default:
		return false
	}
}

func newAsyncImageContext(c *gin.Context, body []byte, timeoutDuration time.Duration) (*gin.Context, *httptest.ResponseRecorder, context.CancelFunc) {
	base := context.WithoutCancel(c.Request.Context())
	executionCtx, cancel := context.WithTimeout(base, timeoutDuration)
	request := c.Request.Clone(executionCtx)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	request.ContentLength = int64(len(body))
	request.URL.Path = strings.TrimSuffix(request.URL.Path, "/async")

	taskCtx := c.Copy()
	recorder := httptest.NewRecorder()
	recorderCtx, _ := gin.CreateTestContext(recorder)
	taskCtx.Writer = recorderCtx.Writer
	taskCtx.Request = request
	return taskCtx, recorder, cancel
}

func asyncImageRequestStreams(contentType string, body []byte) bool {
	if isMultipartImagesContentType(contentType) {
		return false
	}
	var envelope struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Stream
}

func imageTaskPollURL(submitPath, taskID string) string {
	if strings.HasPrefix(submitPath, "/v1/") {
		return "/v1/images/tasks/" + taskID
	}
	return "/images/tasks/" + taskID
}

func extractImageTaskError(body []byte) json.RawMessage {
	if json.Valid(body) {
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && json.Valid(envelope.Error) {
			return envelope.Error
		}
		return json.RawMessage(body)
	}
	return imageTaskErrorPayload("api_error", "image generation failed")
}

func imageTaskErrorPayload(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(gin.H{"type": errorType, "message": message})
	return data
}

func imageTaskError(c *gin.Context, err error) {
	status := infraerrors.Code(err)
	code := infraerrors.Reason(err)
	message := infraerrors.Message(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(code) == "" {
		code = "IMAGE_TASK_ERROR"
	}
	imageTaskJSONError(c, status, code, message)
}

func imageTaskJSONError(c *gin.Context, status int, code, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"type": code, "code": code, "message": message}})
}
