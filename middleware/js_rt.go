package middleware

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"one-api/common"
	"one-api/model"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

/// Runtime 配置
type JSRuntimeConfig struct {
	Enabled        bool          `json:"enabled"`
	MaxVMCount     int           `json:"max_vm_count"`
	ScriptTimeout  time.Duration `json:"script_timeout"`
	PreScriptPath  string        `json:"pre_script_path"`
	PostScriptPath string        `json:"post_script_path"`
	FetchTimeout   time.Duration `json:"fetch_timeout"`
}

/// 池化
type JSRuntimePool struct {
	pool       chan *goja.Runtime
	maxSize    int
	createFunc func() *goja.Runtime
	scripts    map[string]string
	mu         sync.RWMutex
	httpClient *http.Client
}

/// 上下文
type JSContext struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Body        any               `json:"body"`
	UserAgent   string            `json:"userAgent"`
	RemoteIP    string            `json:"remoteIP"`
	Extra       map[string]any    `json:"extra"`
}

type JSResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

type JSDatabase struct {
	db *gorm.DB
}

type JSFetchRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Timeout int               `json:"timeout"`
}

type JSFetchResponse struct {
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	OK         bool              `json:"ok"`
}

type responseWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	headerMap  http.Header
	written    bool
	mu         sync.RWMutex
}

var (
	jsRuntimePool *JSRuntimePool
	jsPoolOnce    sync.Once
	jsConfig      = JSRuntimeConfig{}
)

const (
	defaultPreScriptPath  = "scripts/pre_process.js"
	defaultPostScriptPath = "scripts/post_process.js"
	defaultScriptTimeout  = 5 * time.Second
	defaultFetchTimeout   = 10 * time.Second
	defaultMaxVMCount     = 8
)

func init() {
	if enabled := os.Getenv("JS_RUNTIME_ENABLED"); enabled != "" {
		jsConfig.Enabled = enabled == "true"
	}

	if maxCount := os.Getenv("JS_MAX_VM_COUNT"); maxCount != "" {
		if count, err := strconv.Atoi(maxCount); err == nil && count > 0 {
			jsConfig.MaxVMCount = count
		}
	} else {
		jsConfig.MaxVMCount = defaultMaxVMCount
	}

	if timeout := os.Getenv("JS_SCRIPT_TIMEOUT"); timeout != "" {
		if t, err := time.ParseDuration(timeout + "s"); err == nil && t > 0 {
			jsConfig.ScriptTimeout = t
		}
	} else {
		jsConfig.ScriptTimeout = defaultScriptTimeout
	}

	if fetchTimeout := os.Getenv("JS_FETCH_TIMEOUT"); fetchTimeout != "" {
		if t, err := time.ParseDuration(fetchTimeout + "s"); err == nil && t > 0 {
			jsConfig.FetchTimeout = t
		}
	} else {
		jsConfig.FetchTimeout = defaultFetchTimeout
	}

	jsConfig.PreScriptPath = os.Getenv("JS_PREPROCESS_SCRIPT_PATH")
	if jsConfig.PreScriptPath == "" {
		jsConfig.PreScriptPath = defaultPreScriptPath
	}

	jsConfig.PostScriptPath = os.Getenv("JS_POSTPROCESS_SCRIPT_PATH")
	if jsConfig.PostScriptPath == "" {
		jsConfig.PostScriptPath = defaultPostScriptPath
	}
}

func parseBodyByType(bodyBytes []byte, contentType string) any {
	if len(bodyBytes) == 0 {
		return ""
	}

	bodyStr := string(bodyBytes)
	contentLower := strings.ToLower(contentType)

	switch {
	case strings.Contains(contentLower, "application/json"):
		var jsonObj any
		if err := json.Unmarshal(bodyBytes, &jsonObj); err == nil {
			return jsonObj
		}
		return bodyStr

	case strings.Contains(contentLower, "application/x-www-form-urlencoded"):
		if values, err := url.ParseQuery(bodyStr); err == nil {
			result := make(map[string]string, len(values))
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}
		return bodyStr

	case strings.Contains(contentLower, "multipart/form-data"):
		return bodyBytes

	case strings.Contains(contentLower, "text/"):
		return bodyStr

	default:
		// 尝试JSON解析
		var jsonObj any
		if json.Unmarshal(bodyBytes, &jsonObj) == nil {
			return jsonObj
		}

		// 尝试form解析
		if values, err := url.ParseQuery(bodyStr); err == nil && len(values) > 0 {
			result := make(map[string]string, len(values))
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}

		return bodyStr
	}
}

func createJSContext(c *gin.Context) *JSContext {
	var bodyBytes []byte
	if c.Request != nil && c.Request.Body != nil {
		bodyBytes, _ = io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	// headers map
	headers := make(map[string]string)
	if c.Request != nil && c.Request.Header != nil {
		for key, values := range c.Request.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
	}

	method := ""
	url := ""
	userAgent := ""
	remoteIP := ""
	contentType := ""

	if c.Request != nil {
		method = c.Request.Method
		if c.Request.URL != nil {
			url = c.Request.URL.String()
		}
		userAgent = c.Request.UserAgent()
		contentType = c.ContentType()
	}

	if c != nil {
		remoteIP = c.ClientIP()
	}

	parsedBody := parseBodyByType(bodyBytes, contentType)

	return &JSContext{
		Method:      method,
		URL:         url,
		Headers:     headers,
		Body:        parsedBody,
		UserAgent:   userAgent,
		RemoteIP:    remoteIP,
		Extra:       make(map[string]any),
	}
}

func NewJSRuntimePool(maxSize int) *JSRuntimePool {
	// 创建HTTP客户端
	httpClient := &http.Client{
		Timeout: jsConfig.FetchTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	pool := &JSRuntimePool{
		pool:       make(chan *goja.Runtime, maxSize),
		maxSize:    maxSize,
		scripts:    make(map[string]string),
		httpClient: httpClient,
	}

	pool.createFunc = func() *goja.Runtime {
		vm := goja.New()
		pool.setupGlobals(vm)
		pool.loadScripts(vm)
		return vm
	}

	// 预创建VM
	preCreate := min(maxSize/2, 4)
	for range preCreate {
		select {
		case pool.pool <- pool.createFunc():
		default:
		}
	}

	return pool
}

func (p *JSRuntimePool) Get() *goja.Runtime {
	select {
	case vm := <-p.pool:
		return vm
	default:
		return p.createFunc()
	}
}

func (p *JSRuntimePool) Put(vm *goja.Runtime) {
	if vm == nil {
		return
	}
	
	select {
	case p.pool <- vm:
	default:
		// 池满，丢弃VM让GC回收
	}
}

func (p *JSRuntimePool) setupGlobals(vm *goja.Runtime) {
	// console
	console := vm.NewObject()
	console.Set("log", func(args ...any) {
		var strs []string
		for _, arg := range args {
			strs = append(strs, fmt.Sprintf("%v", arg))
		}
		common.SysLog("JS: " + strings.Join(strs, " "))
	})
	console.Set("error", func(args ...any) {
		var strs []string
		for _, arg := range args {
			strs = append(strs, fmt.Sprintf("%v", arg))
		}
		common.SysError("JS: " + strings.Join(strs, " "))
	})
	console.Set("warn", func(args ...any) {
		var strs []string
		for _, arg := range args {
			strs = append(strs, fmt.Sprintf("%v", arg))
		}
		common.SysError("JS WARN: " + strings.Join(strs, " "))
	})
	vm.Set("console", console)

	// JSON
	jsonObj := vm.NewObject()
	jsonObj.Set("parse", func(str string) any {
		var result any
		err := json.Unmarshal([]byte(str), &result)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return result
	})
	jsonObj.Set("stringify", func(obj any) string {
		data, err := json.Marshal(obj)
		if err != nil {
			panic(vm.ToValue(err.Error()))
		}
		return string(data)
	})
	vm.Set("JSON", jsonObj)

	// fetch 实现
	vm.Set("fetch", func(url string, options ...any) *JSFetchResponse {
		return p.fetch(url, options...)
	})

	// 数据库
	vm.Set("db", &JSDatabase{db: model.DB})

	// 定时器 (简化版)
	vm.Set("setTimeout", func(fn func(), delay int) {
		go func() {
			time.Sleep(time.Duration(delay) * time.Millisecond)
			fn()
		}()
	})
}

func (p *JSRuntimePool) fetch(url string, options ...any) *JSFetchResponse {
	req := &JSFetchRequest{
		Method:  "GET",
		URL:     url,
		Headers: make(map[string]string),
		Timeout: int(jsConfig.FetchTimeout.Seconds()),
	}

	// 解析选项
	if len(options) > 0 && options[0] != nil {
		if optMap, ok := options[0].(map[string]any); ok {
			if method, exists := optMap["method"]; exists {
				if methodStr, ok := method.(string); ok {
					req.Method = strings.ToUpper(methodStr)
				}
			}

			if headers, exists := optMap["headers"]; exists {
				if headersMap, ok := headers.(map[string]any); ok {
					for k, v := range headersMap {
						if vStr, ok := v.(string); ok {
							req.Headers[k] = vStr
						}
					}
				}
			}

			if body, exists := optMap["body"]; exists {
				switch v := body.(type) {
				case string:
					req.Body = v
				case map[string]any:
					if bodyBytes, err := json.Marshal(v); err == nil {
						req.Body = string(bodyBytes)
						req.Headers["Content-Type"] = "application/json"
					}
				default:
					req.Body = fmt.Sprintf("%v", body)
				}
			}

			if timeout, exists := optMap["timeout"]; exists {
				if timeoutNum, ok := timeout.(float64); ok {
					req.Timeout = int(timeoutNum)
				}
			}
		}
	}

	// 创建HTTP请求
	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}

	httpReq, err := http.NewRequest(req.Method, req.URL, bodyReader)
	if err != nil {
		return &JSFetchResponse{
			Status:     0,
			StatusText: err.Error(),
			Headers:    make(map[string]string),
			Body:       "",
			OK:         false,
		}
	}

	// 设置请求头
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	// 设置默认User-Agent
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", "JS-Runtime-Fetch/1.0")
	}

	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout)*time.Second)
	defer cancel()
	httpReq = httpReq.WithContext(ctx)

	// 执行请求
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return &JSFetchResponse{
			Status:     0,
			StatusText: err.Error(),
			Headers:    make(map[string]string),
			Body:       "",
			OK:         false,
		}
	}
	defer resp.Body.Close()

	// 读取响应体
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &JSFetchResponse{
			Status:     resp.StatusCode,
			StatusText: resp.Status,
			Headers:    make(map[string]string),
			Body:       "",
			OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		}
	}

	// 构建响应头
	headers := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	return &JSFetchResponse{
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		Headers:    headers,
		Body:       string(bodyBytes),
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
	}
}

func (p *JSRuntimePool) loadScripts(vm *goja.Runtime) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 加载预处理脚本
	if script, exists := p.scripts["pre"]; exists {
		if _, err := vm.RunString(script); err != nil {
			common.SysError("Failed to load pre_process.js: " + err.Error())
		}
	} else if preScript, err := os.ReadFile(jsConfig.PreScriptPath); err == nil {
		p.scripts["pre"] = string(preScript)
		if _, err = vm.RunString(string(preScript)); err != nil {
			common.SysError("Failed to load pre_process.js: " + err.Error())
		} else {
			common.SysLog("Loaded pre_process.js")
		}
	}

	// 加载后处理脚本
	if script, exists := p.scripts["post"]; exists {
		if _, err := vm.RunString(script); err != nil {
			common.SysError("Failed to load post_process.js: " + err.Error())
		}
	} else if postScript, err := os.ReadFile(jsConfig.PostScriptPath); err == nil {
		p.scripts["post"] = string(postScript)
		if _, err = vm.RunString(string(postScript)); err != nil {
			common.SysError("Failed to load post_process.js: " + err.Error())
		} else {
			common.SysLog("Loaded post_process.js")
		}
	}
}

func (p *JSRuntimePool) ReloadScripts() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 清空缓存的脚本
	p.scripts = make(map[string]string)

	// 清空VM池，强制重新创建
	for {
		select {
		case <-p.pool:
		default:
			goto done
		}
	}
done:
	common.SysLog("JavaScript scripts reloaded")
}

func (jsdb *JSDatabase) Query(sql string, args ...any) []map[string]any {
	if jsdb.db == nil {
		common.SysError("JS DB is nil")
		return nil
	}

	rows, err := jsdb.db.Raw(sql, args...).Rows()
	if err != nil {
		common.SysError("JS DB Query Error: " + err.Error())
		return nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		common.SysError("JS DB Columns Error: " + err.Error())
		return nil
	}

	results := make([]map[string]any, 0, 100)
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			common.SysError("JS DB Scan Error: " + err.Error())
			continue
		}

		row := make(map[string]any, len(columns))
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	return results
}

func (jsdb *JSDatabase) Exec(sql string, args ...any) map[string]any {
	if jsdb.db == nil {
		return map[string]any{
			"rowsAffected": int64(0),
			"error":        "database is nil",
		}
	}

	result := jsdb.db.Exec(sql, args...)
	return map[string]any{
		"rowsAffected": result.RowsAffected,
		"error":        result.Error,
	}
}

func initJSRuntimePool() *JSRuntimePool {
	jsPoolOnce.Do(func() {
		jsRuntimePool = NewJSRuntimePool(jsConfig.MaxVMCount)
		common.SysLog("JavaScript runtime pool initialized successfully")
	})
	return jsRuntimePool
}

func validateGinContext(c *gin.Context) error {
	if c == nil {
		return fmt.Errorf("gin context is nil")
	}
	if c.Request == nil {
		return fmt.Errorf("gin context request is nil")
	}
	return nil
}

func (p *JSRuntimePool) executeWithTimeout(vm *goja.Runtime, fn func() (goja.Value, error)) (goja.Value, error) {
	type result struct {
		value goja.Value
		err   error
	}

	resultChan := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultChan <- result{err: fmt.Errorf("JS panic: %v", r)}
			}
		}()
		
		value, err := fn()
		resultChan <- result{value: value, err: err}
	}()

	select {
	case res := <-resultChan:
		return res.value, res.err
	case <-time.After(jsConfig.ScriptTimeout):
		return nil, fmt.Errorf("script execution timeout after %v", jsConfig.ScriptTimeout)
	}
}

func (p *JSRuntimePool) PreProcessRequest(c *gin.Context) error {
	if err := validateGinContext(c); err != nil {
		common.SysError("JS PreProcess Validation Error: " + err.Error())
		return err
	}

	vm := p.Get()
	defer p.Put(vm)

	preProcessFunc := vm.Get("preProcessRequest")
	if preProcessFunc == nil || goja.IsUndefined(preProcessFunc) {
		return nil
	}

	jsCtx := createJSContext(c)
	if jsCtx == nil {
		return fmt.Errorf("failed to create JS context")
	}

	result, err := p.executeWithTimeout(vm, func() (goja.Value, error) {
		vm.Set("ctx", jsCtx)
		fn, ok := goja.AssertFunction(preProcessFunc)
		if !ok {
			return nil, fmt.Errorf("preProcessRequest is not a function")
		}
		return fn(goja.Undefined(), vm.ToValue(jsCtx))
	})

	if err != nil {
		common.SysError("JS PreProcess Error: " + err.Error())
		return err
	}

	// 处理返回结果
	if result != nil && !goja.IsUndefined(result) {
		resultObj := result.Export()
		if resultMap, ok := resultObj.(map[string]any); ok {
			// 是否修改请求
			if newBody, exists := resultMap["body"]; exists {
				switch v := newBody.(type) {
				case string:
					c.Request.Body = io.NopCloser(strings.NewReader(v))
					c.Request.ContentLength = int64(len(v))
				case []byte:
					c.Request.Body = io.NopCloser(bytes.NewBuffer(v))
					c.Request.ContentLength = int64(len(v))
				case map[string]any:
					bodyBytes, err := json.Marshal(v)
					if err == nil {
						c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
						c.Request.ContentLength = int64(len(bodyBytes))
						c.Request.Header.Set("Content-Type", "application/json")
					} else {
						common.SysError("JS PreProcess JSON Marshal Error: " + err.Error())
					}
				default:
					common.SysError("JS PreProcess Unsupported Body Type: " + fmt.Sprintf("%T", newBody))
				}
			}

			// 是否修改 headers
			if newHeaders, exists := resultMap["headers"]; exists {
				if headersMap, ok := newHeaders.(map[string]any); ok {
					for key, value := range headersMap {
						if valueStr, ok := value.(string); ok {
							c.Request.Header.Set(key, valueStr)
						}
					}
				}
			}

			// 是否阻止请求
			if block, exists := resultMap["block"]; exists {
				if blockBool, ok := block.(bool); ok && blockBool {
					status := http.StatusForbidden
					if statusCode, exists := resultMap["statusCode"]; exists {
						if statusInt, ok := statusCode.(float64); ok {
							status = int(statusInt)
						}
					}

					message := "Request blocked by pre-process script"
					if msg, exists := resultMap["message"]; exists {
						if msgStr, ok := msg.(string); ok {
							message = msgStr
						}
					}

					c.JSON(status, gin.H{"error": message})
					c.Abort()
					return fmt.Errorf("request blocked")
				}
			}
		}
	}

	return nil
}

func (p *JSRuntimePool) PostProcessResponse(c *gin.Context, statusCode int, body []byte) (int, []byte, error) {
	if err := validateGinContext(c); err != nil {
		common.SysError("JS PostProcess Validation Error: " + err.Error())
		return statusCode, body, err
	}

	vm := p.Get()
	defer p.Put(vm)

	postProcessFunc := vm.Get("postProcessResponse")
	if postProcessFunc == nil || goja.IsUndefined(postProcessFunc) {
		return statusCode, body, nil
	}

	jsCtx := createJSContext(c)
	if jsCtx == nil {
		return statusCode, body, fmt.Errorf("failed to create JS context")
	}

	jsResponse := &JSResponse{
		StatusCode: statusCode,
		Headers:    make(map[string]string),
		Body:       string(body),
	}

	// 获取响应头
	if c.Writer != nil {
		for key, values := range c.Writer.Header() {
			if len(values) > 0 {
				jsResponse.Headers[key] = values[0]
			}
		}
	}

	result, err := p.executeWithTimeout(vm, func() (goja.Value, error) {
		vm.Set("ctx", jsCtx)
		fn, ok := goja.AssertFunction(postProcessFunc)
		if !ok {
			return nil, fmt.Errorf("postProcessResponse is not a function")
		}
		return fn(goja.Undefined(), vm.ToValue(jsCtx), vm.ToValue(jsResponse))
	})

	if err != nil {
		common.SysError("JS PostProcess Error: " + err.Error())
		return statusCode, body, err
	}

	// 处理返回
	if result != nil && !goja.IsUndefined(result) {
		resultObj := result.Export()
		if resultMap, ok := resultObj.(map[string]any); ok {
			if newStatusCode, exists := resultMap["statusCode"]; exists {
				if statusInt, ok := newStatusCode.(float64); ok {
					statusCode = int(statusInt)
				}
			}

			if newBody, exists := resultMap["body"]; exists {
				if bodyStr, ok := newBody.(string); ok {
					body = []byte(bodyStr)
				}
			}

			if newHeaders, exists := resultMap["headers"]; exists {
				if headersMap, ok := newHeaders.(map[string]any); ok {
					for key, value := range headersMap {
						if valueStr, ok := value.(string); ok {
							c.Header(key, valueStr)
						}
					}
				}
			}
		}
	}

	return statusCode, body, nil
}

func (p *JSRuntimePool) hasPostProcessFunction() bool {
	vm := p.Get()
	defer p.Put(vm)
	postProcessFunc := vm.Get("postProcessResponse")
	return postProcessFunc != nil && !goja.IsUndefined(postProcessFunc)
}

func newResponseWriter(w gin.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		statusCode:     200,
		headerMap:      make(http.Header),
		written:        false,
	}
}

func (w *responseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if !w.written {
		w.WriteHeader(200)
	}
	return w.body.Write(data)
}

func (w *responseWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if !w.written {
		w.WriteHeader(200)
	}
	return w.body.WriteString(s)
}

func (w *responseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if w.written {
		return
	}
	w.statusCode = statusCode
	w.written = true

	maps.Copy(w.headerMap, w.ResponseWriter.Header())
}

func (w *responseWriter) Header() http.Header {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	if w.headerMap == nil {
		w.headerMap = make(http.Header)
	}
	return w.headerMap
}

func JSRuntimeMiddleware() gin.HandlerFunc {
	if !jsConfig.Enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	pool := initJSRuntimePool()
	return func(c *gin.Context) {
		start := time.Now()

		// 预处理
		common.SysLog("JS Runtime PreProcessing Request: " + c.Request.Method + " " + c.Request.URL.String())
		if err := pool.PreProcessRequest(c); err != nil {
			common.SysError("JS Runtime PreProcess Error: " + err.Error())
			return
		}
		common.SysLog("JS Runtime PreProcessing Completed")

		// 后处理
		if pool.hasPostProcessFunction() {
			common.SysLog("JS Runtime PostProcessing Response")
			writer := newResponseWriter(c.Writer)
			c.Writer = writer

			c.Next()

			// 后处理响应
			if writer.body.Len() > 0 {
				statusCode, body, err := pool.PostProcessResponse(c, writer.statusCode, writer.body.Bytes())
				if err == nil {
					// 更新响应
					c.Writer = writer.ResponseWriter
					c.Writer.Header().Del("Content-Length")

					// 设置修改后的headers
					for k, v := range writer.headerMap {
						for _, value := range v {
							c.Writer.Header().Add(k, value)
						}
					}

					c.Status(statusCode)
					c.Writer.Write(body)
					common.SysLog(fmt.Sprintf("JS Runtime PostProcessing Completed with status %d", statusCode))
				} else {
					// 出错时返回原始响应
					c.Writer = writer.ResponseWriter
					c.Writer.Header().Del("Content-Length")
					c.Status(writer.statusCode)
					c.Writer.Write(writer.body.Bytes())
					common.SysError(fmt.Sprintf("JS Runtime PostProcess Error: %v", err))
				}
			} else {
				// 没有响应体时，恢复原始writer
				c.Writer = writer.ResponseWriter
				common.SysLog("JS Runtime PostProcessing Completed with no body")
			}
		} else {
			c.Next()
			common.SysLog("JS Runtime PostProcessing Skipped: No postProcessResponse function defined")
		}

		// 记录处理时间
		duration := time.Since(start)
		if duration > time.Millisecond*100 {
			common.SysLog(fmt.Sprintf("JS Runtime processing took %v", duration))
		}
	}
}

func ReloadJSScripts() {
	if jsRuntimePool != nil {
		jsRuntimePool.ReloadScripts()
		common.SysLog("JavaScript scripts reloaded")
	}
}