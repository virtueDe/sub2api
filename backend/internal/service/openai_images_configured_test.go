//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func configuredImageAccount(model string) *Account {
	return &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"api_key": "image-key", "base_url": "https://compatible.example/v1",
			"openai_capabilities": []string{"images"}, OpenAIImageModelsCredentialKey: []string{model},
		}}
}

func TestConfiguredImagesAccountAdmission(t *testing.T) {
	for _, model := range []string{"seedream-5.5", "vendor-art-v9"} {
		t.Run(model, func(t *testing.T) {
			account := configuredImageAccount(model)
			require.NoError(t, NormalizeOpenAIImageCredentials(account.Platform, account.Type, account.Credentials))
			require.True(t, account.SupportsOpenAIImageModel(model))
			require.False(t, account.SupportsOpenAIImageModel("unconfigured-model"))
			require.False(t, account.SupportsOpenAIImageModel("gpt-5.4"))
			require.False(t, account.SupportsOpenAIImageModel("gpt-image-2"), "declared compatible image accounts serve only configured targets")
			require.False(t, isOpenAIImageGenerationModel(model))
			account.Credentials["model_mapping"] = map[string]any{"paint": model, "gpt-image-alias": model, "art-*": model}
			for _, publicID := range []string{"paint", "gpt-image-alias", "art-custom"} {
				require.True(t, account.SupportsOpenAIImageModel(publicID))
				require.True(t, accountSupportsOpenAICapabilities(account, "", OpenAIImagesCapabilityAPIKey, publicID))
			}
			require.Equal(t, []string{"gpt-image-alias", "paint"}, account.OpenAIImageModelIDs())
			for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
				account.Type = typ
				require.False(t, account.HasOpenAICompatibleImages())
				require.False(t, accountSupportsOpenAICapabilities(account, "", OpenAIImagesCapabilityNative, "gpt-image-alias"))
				require.False(t, accountSupportsOpenAICapabilities(account, "", OpenAIImagesCapabilityAPIKey, model))
				require.Error(t, NormalizeOpenAIImageCredentials(account.Platform, account.Type, account.Credentials))
			}
			account.Type = AccountTypeAPIKey
			account.Credentials["openai_capabilities"] = []string{"chat_completions"}
			require.False(t, account.SupportsOpenAIImageModel("paint"))
		})
	}
}

type configuredImageListRepo struct {
	AccountRepository
	accounts []Account
}

func (r configuredImageListRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return r.accounts, nil
}

func (r configuredImageListRepo) ListSchedulable(context.Context) ([]Account, error) {
	return r.accounts, nil
}

func TestConfiguredImagesModelListing(t *testing.T) {
	account := configuredImageAccount("seedream-5.5")
	account.Credentials[OpenAIImageModelsCredentialKey] = []string{"seedream-5.5", "vendor-art-v9"}
	svc := &GatewayService{accountRepo: configuredImageListRepo{accounts: []Account{*account}}}
	expected := []string{"seedream-5.5", "vendor-art-v9"}
	require.Equal(t, expected, svc.GetAvailableModels(context.Background(), i64p(1), PlatformOpenAI))
	require.Equal(t, expected, svc.GetAvailableOpenAIImageModels(context.Background(), i64p(1), PlatformOpenAI, []string{"gpt-image-2"}))
	require.True(t, explicitModelMappingClaims(*account, "seedream-5.5"))
	require.False(t, explicitModelMappingClaims(*account, "gpt-image-2"))
	models := AppendOpenAIImageModels(nil, account)
	require.Len(t, models, 2)
	account.Credentials["model_mapping"] = map[string]any{"paint": "seedream-5.5", "chat": "text-only"}
	require.Equal(t, []string{"paint"}, account.OpenAIImageModelIDs())
	require.Len(t, AppendOpenAIImageModels(nil, account), 1)
	account.Credentials["model_mapping"] = map[string]any{"chat": "text-only"}
	emptySvc := &GatewayService{accountRepo: configuredImageListRepo{accounts: []Account{*account}}}
	empty := emptySvc.GetAvailableModels(context.Background(), i64p(1), PlatformOpenAI)
	require.NotNil(t, empty, "an explicitly empty image catalog must not restore default GPT models")
	require.Empty(t, empty)
}

func TestConfiguredImagesCredentialValidation(t *testing.T) {
	for _, raw := range []any{[]any{123}, "seedream-5.5", []string{""}, []string{"vendor-*"}, []string{"model\nheader"}} {
		account := configuredImageAccount("seedream-5.5")
		account.Credentials[OpenAIImageModelsCredentialKey] = raw
		require.Error(t, NormalizeOpenAIImageCredentials(account.Platform, account.Type, account.Credentials))
	}
	account := configuredImageAccount("seedream-5.5")
	account.Credentials[OpenAIImageModelsCredentialKey] = []any{" seedream-5.5 ", "seedream-5.5", "vendor-art-v9"}
	account.Credentials["openai_capabilities"] = map[string]any{"images": true}
	// Create-account validation and API writes use the same normalization.
	created, err := buildAccountForCreate(&CreateAccountInput{Platform: account.Platform, Type: account.Type, Credentials: account.Credentials}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, []string{"seedream-5.5", "vendor-art-v9"}, created.ConfiguredOpenAIImageModels())
	delete(account.Credentials, "base_url")
	require.ErrorContains(t, NormalizeOpenAIImageCredentials(account.Platform, account.Type, account.Credentials), "base URL")
	account.Credentials["base_url"] = "https://compatible.example"
	account.Credentials[OpenAIImageModelsCredentialKey] = []string{}
	require.ErrorContains(t, NormalizeOpenAIImageCredentials(account.Platform, account.Type, account.Credentials), "at least one")
	capabilities, _, err := normalizeBulkOpenAIEndpointCapabilities([]any{"chat_completions", "images"})
	require.NoError(t, err)
	require.Equal(t, []string{"chat_completions", "images"}, capabilities)
}

func TestConfiguredImagesBulkRejectsRemovingBaseURL(t *testing.T) {
	for _, value := range []any{nil, ""} {
		repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{configuredImageAccount("seedream-5.5")}}
		svc := &adminServiceImpl{accountRepo: repo}
		_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{7}, Credentials: map[string]any{"base_url": value}})
		require.ErrorContains(t, err, "base URL")
		require.Zero(t, repo.bulkUpdateCalls)
	}
}

func TestConfiguredImagesTestRejectsUndeclaredNativeModel(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	err := svc.testOpenAIAccountConnection(c, configuredImageAccount("seedream-5.5"), "gpt-image-2", "draw", AccountTestModeDefault)
	require.ErrorContains(t, err, "not configured")
	require.Empty(t, upstream.requests)
}

func TestConfiguredImagesPinnedCatalogAndMixedDiscovery(t *testing.T) {
	account := configuredImageAccount("seedream-5.5")
	account.Credentials["model_mapping"] = map[string]any{"paint": "seedream-5.5", "gpt-image-2": "gpt-image-2"}
	// Image-only catalogs are explicit configuration, so no /models request is needed.
	svc := &OpenAIGatewayService{accountRepo: splitCodexModelsAccountRepo{all: map[int64][]Account{21: {*account}}}}
	group := &Group{ID: 21, Platform: PlatformOpenAI, CodexModelsManifestConfig: GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{7}}}
	response, _, err := svc.FetchPinnedOpenAIModelsList(context.Background(), group, 0, "")
	require.NoError(t, err)
	require.Contains(t, string(response.Body), `"id":"paint"`)
	require.NotContains(t, string(response.Body), `"id":"gpt-image-2"`)
	group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"other"}}
	response, _, err = svc.FetchPinnedOpenAIModelsList(context.Background(), group, 0, "")
	require.NoError(t, err)
	require.Empty(t, gjson.GetBytes(response.Body, "data").Array())

	account.Credentials["openai_capabilities"] = []string{"chat_completions", "images"}
	delete(account.Credentials, "model_mapping")
	mixed := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		return ordinaryModelsUpstreamResponse(`{"data":[{"id":"chat-model","provider_metadata":"kept"}]}`), nil
	}})
	response, err = mixed.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	require.Contains(t, string(response.Body), `"id":"seedream-5.5"`)
	require.Contains(t, string(response.Body), `"provider_metadata":"kept"`)
}

func TestConfiguredImagesForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"seedream-5.5", "vendor-art-v9"} {
		for _, kind := range []string{"direct", "alias", "channel", "multipart"} {
			for _, format := range []string{"url", "b64_json"} {
				t.Run(model+"/"+kind+"/"+format, func(t *testing.T) {
					account := configuredImageAccount(model)
					publicID, channelID := model, ""
					if kind == "alias" {
						publicID = "paint"
						account.Credentials["model_mapping"] = map[string]any{publicID: model}
					}
					if kind == "channel" {
						publicID, channelID = "gpt-image-alias", model
					}
					endpoint, contentType := openAIImagesGenerationsEndpoint, "application/json"
					body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","n":2,"size":"2K","response_format":%q,"provider_option":{"watermark":false}}`, publicID, format))
					if kind == "multipart" {
						endpoint = openAIImagesEditsEndpoint
						var buf bytes.Buffer
						writer := multipart.NewWriter(&buf)
						require.NoError(t, writer.WriteField("model", model))
						require.NoError(t, writer.WriteField("prompt", "draw"))
						require.NoError(t, writer.WriteField("provider_option", "preserved"))
						file, err := writer.CreateFormFile("image", "input.png")
						require.NoError(t, err)
						_, err = file.Write([]byte("original-file"))
						require.NoError(t, err)
						require.NoError(t, writer.Close())
						body, contentType = buf.Bytes(), writer.FormDataContentType()
					}
					payload := `{"data":[{"b64_json":"aW1hZ2U="},{"b64_json":"aW1hZ2U="}]}`
					if format == "url" {
						payload = `{"data":[{"url":"https://cdn.example/one.png"},{"url":"https://cdn.example/two.png"}]}`
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
					c.Request.Header.Set("Content-Type", contentType)
					parsed, err := svc.ParseOpenAIImagesRequest(c, body)
					require.NoError(t, err)
					result, err := svc.ForwardImages(c.Request.Context(), c, account, body, parsed, channelID)
					require.NoError(t, err)
					require.Equal(t, 2, result.ImageCount)
					require.True(t, result.ImageEndpoint)
					require.Equal(t, model, result.UpstreamModel)
					require.Equal(t, "https://compatible.example"+endpoint, upstream.lastReq.URL.String())
					require.Equal(t, "Bearer image-key", upstream.lastReq.Header.Get("Authorization"))
					if kind == "multipart" {
						r := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(upstream.lastBody))
						r.Header = upstream.lastReq.Header
						require.NoError(t, r.ParseMultipartForm(1<<20))
						t.Cleanup(func() { _ = r.MultipartForm.RemoveAll() })
						require.Equal(t, model, r.FormValue("model"))
						require.Equal(t, "preserved", r.FormValue("provider_option"))
					} else {
						require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
						require.Equal(t, "2K", gjson.GetBytes(upstream.lastBody, "size").String())
						require.Equal(t, gjson.False, gjson.GetBytes(upstream.lastBody, "provider_option.watermark").Type)
					}
				})
			}
		}
	}
}

func TestConfiguredImagesAccountTestAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"seedream-5.5", "vendor-art-v9"} {
		for _, format := range []string{"url", "b64_json", "empty"} {
			t.Run(model+"/"+format, func(t *testing.T) {
				payload := `{"data":[{"url":"https://cdn.example/image.png"}]}`
				if format == "b64_json" {
					payload = `{"data":[{"b64_json":"aW1hZ2U="}]}`
				} else if format == "empty" {
					payload = `{"data":[{"revised_prompt":"no image"}]}`
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}}
				account := configuredImageAccount(model)
				account.Credentials["model_mapping"] = map[string]any{"paint": model}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/7/test", nil)
				svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
				err := svc.testOpenAIAccountConnection(c, account, "paint", "draw", AccountTestModeDefault)
				if format == "empty" {
					require.Error(t, err)
					require.NotContains(t, rec.Body.String(), `"success":true`)
					return
				}
				require.NoError(t, err)
				require.Equal(t, "/v1/images/generations", upstream.lastReq.URL.Path)
				require.Contains(t, rec.Body.String(), `"type":"image"`)
				require.Contains(t, rec.Body.String(), `"success":true`)
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
			})
		}
		t.Run(model+"/billing", func(t *testing.T) {
			price := 0.21
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "configured-" + model, Model: model, UpstreamModel: model, ImageEndpoint: true, ImageCount: 2, ImageSize: "1K", Duration: time.Second},
				APIKey: &APIKey{ID: 1, GroupID: i64p(1), Group: &Group{ID: 1, ImageRateIndependent: true, ImageRateMultiplier: 1, ImagePrice1K: &price}},
				User:   &User{ID: 2}, Account: configuredImageAccount(model),
			})
			require.NoError(t, err)
			require.NotNil(t, repo.lastLog)
			require.Equal(t, model, repo.lastLog.Model)
			require.Equal(t, 2, repo.lastLog.ImageCount)
			require.InDelta(t, 0.42, repo.lastLog.TotalCost, 1e-12)
			require.Equal(t, string(BillingModeImage), *repo.lastLog.BillingMode)
		})
	}
}
