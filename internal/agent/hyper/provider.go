// Package hyper provides fantasy.提供方 那 proxies requests 到 Hyper.
package hyper

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy/providers/openai"
)

//go:generate wget -O provider.json https://hyper.charm.land/v1/provider/v3

//go:embed provider.json
var embedded []byte

// Embedded returns embedded Hyper 提供方.
var Embedded = sync.OnceValue(func() catwalk.Provider {
	var provider catwalk.Provider
	if err := json.Unmarshal(embedded, &provider); err != nil {
		slog.Error("Could not use embedded provider data", "err", err)
	}
	if e := os.Getenv("HYPER_URL"); e != "" {
		provider.APIEndpoint = e + "/api/v1/fantasy"
	}
	return provider
})

const (
	// Name 是 默认 name 的 这 meta 提供方.
	Name = "hyper"
	// DisplayName 是 display name 的 Hyper.
	DisplayName = "Charm Hyper"
	// defaultBaseURL 是 默认 proxy URL.
	defaultBaseURL = "https://hyper.charm.land"
)

// BaseURL returns base URL, which 是 either $HYPER_URL 或 默认.
var BaseURL = sync.OnceValue(func() string {
	return cmp.Or(os.Getenv("HYPER_URL"), defaultBaseURL)
})

// Prism router headers, present 在 responses served through Prism 模型
// (模型 router). They 报告 which 模型 actually answered 请求.
const (
	// PrismModelIDHeader 是 header carrying routed 模型 ID.
	PrismModelIDHeader = "X-Prism-Model-Id"
	// PrismModelNameHeader 是 header carrying routed 模型 name.
	PrismModelNameHeader = "X-Prism-Model-Name"
)

// Prism savings headers. They 是 只 known once 请求 has been
// served, 所以 在 streaming responses they arrive 作为 HTTP trailers.
const (
	// PrismHypercreditSavingsHeader 是 header (或 trailer) carrying
	// hypercredits saved 通过 routing through Prism.
	PrismHypercreditSavingsHeader = "X-Prism-Hypercredit-Savings"
	// PrismDollarSavingsHeader 是 header (或 trailer) carrying
	// dollars saved 通过 routing through Prism.
	PrismDollarSavingsHeader = "X-Prism-Dollar-Savings"
)

// Keys under which Prism router headers 是 存储 在
// [openai.ProviderMetadata.ExtraFields].
const (
	// PrismModelIDField 是 extra 字段 为 routed 模型 ID.
	PrismModelIDField = "x-prism-model-id"
	// PrismModelNameField 是 extra 字段 为 routed 模型 name.
	PrismModelNameField = "x-prism-model-name"
	// PrismHypercreditSavingsField 是 extra 字段 为
	// hypercredits saved 通过 routing through Prism.
	PrismHypercreditSavingsField = "x-prism-hypercredit-savings"
	// PrismDollarSavingsField 是 extra 字段 为 dollars saved
	// 通过 routing through Prism.
	PrismDollarSavingsField = "x-prism-dollar-savings"
)

// HeaderFunc copies Prism router headers 和 trailers into openai
// 提供方 metadata. 它 是 wired 作为 openai-compat language 模型 header
// func 所以 turns served through Prism 模型 报告 which 模型 actually
// answered 和 how much was saved.
func HeaderFunc(header http.Header, metadata *openai.ProviderMetadata) {
	copyHeaderField(header, metadata, PrismModelIDHeader, PrismModelIDField)
	copyHeaderField(header, metadata, PrismModelNameHeader, PrismModelNameField)
	copyHeaderField(header, metadata, PrismHypercreditSavingsHeader, PrismHypercreditSavingsField)
	copyHeaderField(header, metadata, PrismDollarSavingsHeader, PrismDollarSavingsField)
}

func copyHeaderField(header http.Header, metadata *openai.ProviderMetadata, headerName, fieldName string) {
	value := header.Get(headerName)
	if value == "" {
		return
	}
	if metadata.ExtraFields == nil {
		metadata.ExtraFields = make(map[string]json.RawMessage)
	}
	metadata.ExtraFields[fieldName] = json.RawMessage(strconv.Quote(value))
}

// balanceValue stores hypercredit balance most recently fetched 从
// /v1/credits 端点; balanceKnown tracks whether any fetch has
// reported one.
var (
	balanceValue atomic.Int64
	balanceKnown atomic.Bool
)

// setBalance stores hypercredit balance reported 通过 /v1/credits
// 端点.
func setBalance(balance int) {
	balanceValue.Store(int64(balance))
	balanceKnown.Store(true)
}

// clearBalance forgets 存储 balance, 所以 team 那 turns
// hypercredit display off stops showing a stale figure.
func clearBalance() {
	balanceKnown.Store(false)
}

// Balance returns hypercredit balance most recently fetched 从
// /v1/credits endpoint, 或 nil 当 no fetch has reported one yet. That
// includes teams 用 hypercredit display disabled: their balance 是
// reported 在 dollars instead, 所以 there 是 never hypercredit figure 到
// show.
func Balance() *int {
	if !balanceKnown.Load() {
		return nil
	}
	balance := int(balanceValue.Load())
	return &balance
}

// FetchCredits fetches remaining hypercredit balance 从
// /v1/credits endpoint 和 stores it 为 [Balance] 到 return. 它 returns
// nil 当 team has hypercredit display disabled: Hyper reports
// balance 在 dollars 为 them, 所以 there 是 no hypercredit figure 到 show.
func FetchCredits(ctx context.Context, apiKey string) (*int, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		BaseURL()+"/v1/credits",
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Teams 用 hypercredit display disabled get balance_usd 字段
	// instead 的 balance, 和 no balance 是 shown 为 them 在 all.
	var result struct {
		Balance *int `json:"balance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Balance == nil {
		clearBalance()
		return nil, nil
	}
	setBalance(*result.Balance)
	return result.Balance, nil
}
