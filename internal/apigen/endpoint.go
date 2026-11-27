// Package apigen provides fluent API 为 documenting HTTP endpoints.
//
// An [Endpoint] 是 single source 的 truth 为 one route: same
// 值 drives mux registration 和 documentation generation, 所以
// two cannot drift. Endpoints 是 constructed 用 method builders
// ([Get], [Post], [Put], [删除]) 和 finalized 用
// [endpointBuilder.Handle], 只 way 到 produce Endpoint. 一个
// chain without Handle does 不 compile into []Endpoint, 所以
// incomplete definitions fail 在 build time.
package apigen

import "net/http"

// Param documents single 路径 或 查询 parameter.
type Param struct {
	// Name 是 parameter name 作为 it appears 在 路径 或 查询.
	Name string
	// In 是 "路径" 或 "查询".
	In string
	// Type 是 JSON schema type, e.g. "string", "integer".
	Type string
	// Description 是 short human-readable description.
	Description string
	// Required reports whether parameter must 是 provided. 路径
	// parameters 是 always required.
	Required bool
}

// Endpoint 是 documentation 和 routing record 为 single HTTP
// endpoint. Construct it 用 fluent builders ([Get], [Post], ...);
// 只 [endpointBuilder.Handle] produces 完成 Endpoint.
type Endpoint struct {
	method      string
	path        string
	summary     string
	description string
	tags        []string
	params      []Param
	request     any
	response    any
	status      int
	errors      []int
	sse         bool
	handler     http.HandlerFunc
}

// 方法 returns HTTP 方法, e.g. "GET".
func (e Endpoint) Method() string { return e.method }

// 路径 returns route pattern, e.g. "/v1/workspaces/{id}".
func (e Endpoint) Path() string { return e.path }

// 摘要 returns one-line 摘要.
func (e Endpoint) Summary() string { return e.summary }

// Description returns the optional longer prose description.
func (e Endpoint) Description() string { return e.description }

// Tags returns the grouping tags.
func (e Endpoint) Tags() []string { return e.tags }

// Params returns documented 路径 和 查询 parameters.
func (e Endpoint) Params() []Param { return e.params }

// 请求 returns 请求 body example 值, 或 nil 为 none.
func (e Endpoint) Request() any { return e.request }

// 响应 returns success 响应 example 值, 或 nil 为
// empty success 响应.
func (e Endpoint) Response() any { return e.response }

// Status returns success status code, defaulting 到 200.
func (e Endpoint) Status() int {
	if e.status == 0 {
		return http.StatusOK
	}
	return e.status
}

// Errors returns status codes 那 return 错误 object.
func (e Endpoint) Errors() []int { return e.errors }

// SSE reports whether endpoint streams 文本/事件-流 instead
// 的 producing JSON 响应.
func (e Endpoint) SSE() bool { return e.sse }

// 处理器 returns endpoint's HTTP 处理器.
func (e Endpoint) Handler() http.HandlerFunc { return e.handler }

// endpointBuilder accumulates endpoint documentation fluently. 它 是
// deliberately distinct type 从 [Endpoint]: []Endpoint literal
// cannot contain builder, 所以 chain missing Handle fails 到
// compile.
type endpointBuilder struct{ e Endpoint }

// Get begins GET endpoint 为 路径, e.g. Get("/v1/workspaces/{id}").
func Get(path string) endpointBuilder {
	return endpointBuilder{Endpoint{method: http.MethodGet, path: path}}
}

// Post begins POST endpoint 为 路径.
func Post(path string) endpointBuilder {
	return endpointBuilder{Endpoint{method: http.MethodPost, path: path}}
}

// Put begins PUT endpoint 为 路径.
func Put(path string) endpointBuilder {
	return endpointBuilder{Endpoint{method: http.MethodPut, path: path}}
}

// 删除 begins 删除 endpoint 为 路径.
func Delete(path string) endpointBuilder {
	return endpointBuilder{Endpoint{method: http.MethodDelete, path: path}}
}

// 摘要 sets one-line 摘要.
func (b endpointBuilder) Summary(s string) endpointBuilder { b.e.summary = s; return b }

// Description sets an optional longer prose description.
func (b endpointBuilder) Description(s string) endpointBuilder { b.e.description = s; return b }

// Tags sets grouping tags used 在 docs page.
func (b endpointBuilder) Tags(t ...string) endpointBuilder { b.e.tags = t; return b }

// Accepts documents 请求 body 用 example 值, e.g.
// Accepts(proto.工作区{}). 值's type 是 used 到 derive
// JSON schema; reference 是 compile-checked.
func (b endpointBuilder) Accepts(v any) endpointBuilder { b.e.request = v; return b }

// Responds documents success 响应 body 用 example 值.
func (b endpointBuilder) Responds(v any) endpointBuilder { b.e.response = v; return b }

// Status sets success status code 当 it differs 从 200, e.g.
// 202 为 accepted-但-async operation.
func (b endpointBuilder) Status(code int) endpointBuilder { b.e.status = code; return b }

// Fails documents status codes 那 return 错误 object.
func (b endpointBuilder) Fails(codes ...int) endpointBuilder { b.e.errors = codes; return b }

// SSE marks endpoint 作为 streaming 文本/事件-流.
func (b endpointBuilder) SSE() endpointBuilder { b.e.sse = true; return b }

// PathParam documents 路径 parameter. 路径 parameters 是 always
// required.
func (b endpointBuilder) PathParam(name, desc string) endpointBuilder {
	return b.param(Param{Name: name, In: "path", Type: "string", Description: desc, Required: true})
}

// QueryParam documents optional 查询 parameter.
func (b endpointBuilder) QueryParam(name, desc string) endpointBuilder {
	return b.param(Param{Name: name, In: "query", Type: "string", Description: desc})
}

// RequiredQueryParam documents required 查询 parameter.
func (b endpointBuilder) RequiredQueryParam(name, desc string) endpointBuilder {
	return b.param(Param{Name: name, In: "query", Type: "string", Description: desc, Required: true})
}

func (b endpointBuilder) param(p Param) endpointBuilder {
	b.e.params = append(b.e.params, p)
	return b
}

// Handle finalizes endpoint 用 它的 HTTP 处理器.
func (b endpointBuilder) Handle(h http.HandlerFunc) Endpoint {
	b.e.handler = h
	return b.e
}
